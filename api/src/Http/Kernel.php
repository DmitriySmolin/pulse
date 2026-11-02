<?php

declare(strict_types=1);

namespace Pulse\Http;

use Pulse\Config;
use Pulse\ConfigException;
use Pulse\Controller\DebugController;
use Pulse\Controller\HealthController;
use Pulse\Controller\IncidentController;
use Pulse\Controller\MetricsController;
use Pulse\Controller\MonitorController;
use Pulse\Controller\StatusController;
use Pulse\Logger;
use Pulse\Metrics\RequestMetrics;
use Pulse\Repository\CheckRepository;
use Pulse\Repository\IncidentRepository;
use Pulse\Repository\MonitorRepository;
use Pulse\Repository\UptimeRepository;
use Pulse\Services;
use Pulse\Status\StatusService;
use Pulse\Validation\ValidationException;

/**
 * Request lifecycle: handle() -> emit -> fastcgi_finish_request() -> terminate().
 * terminate() records metrics and writes the access log after the client already has the response.
 */
final class Kernel
{
    private const array WRITE_METHODS = ['POST', 'PUT', 'PATCH', 'DELETE'];
    private const array KNOWN_METHODS = ['GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE', 'OPTIONS'];

    /** Successful probe/scrape requests are logged at debug level to keep the log readable. */
    private const array QUIET_ROUTES = ['/healthz', '/readyz', '/metrics'];

    private readonly ?Services $services;
    private readonly ?Router $router;

    /** @param string|null $configError set when the configuration is invalid ($config is null then) */
    public function __construct(
        private readonly ?Config $config,
        private readonly Logger $logger,
        private readonly ?string $configError = null,
    ) {
        $this->services = $config === null ? null : new Services($config, $logger);
        $this->router = $this->services === null ? null : $this->buildRouter($this->services);
    }

    public static function fromEnvironment(): self
    {
        try {
            $config = Config::fromEnvironment();
            $config->assertApiRequirements();

            return new self($config, Logger::stderr($config->logLevel));
        } catch (ConfigException $e) {
            return new self(null, Logger::stderr(), $e->getMessage());
        }
    }

    /** Entry point for public/index.php. */
    public function run(): void
    {
        $request = Request::fromGlobals();
        $this->installErrorHandlers($request);

        $outcome = $this->handle($request);
        $this->emit($request, $outcome->response);
        if (function_exists('fastcgi_finish_request')) {
            fastcgi_finish_request();
        }
        $this->terminate($request, $outcome);
    }

    public function handle(Request $request): Outcome
    {
        $route = RouteMatch::UNMATCHED;
        try {
            if ($this->router === null) {
                [$route, $response] = $this->handleMisconfigured($request);
            } else {
                $match = $this->router->match($request->method, $request->path);
                $route = $match->route;
                $response = $this->dispatch($request, $match);
            }
        } catch (HttpException $e) {
            $response = $e->response;
        } catch (ValidationException $e) {
            $response = Response::error(422, 'validation', ['fields' => $e->fields]);
        } catch (\Throwable $e) {
            $this->logger->error('unhandled exception', ['request_id' => $request->requestId, 'route' => $route] + Logger::exceptionContext($e));
            $response = Response::error(500, 'internal');
        }

        $duration = (hrtime(true) - $request->startedAt) / 1e9;

        return new Outcome($response->withHeader('X-Request-Id', $request->requestId), $route, $duration);
    }

    public function terminate(Request $request, Outcome $outcome): void
    {
        $method = in_array($request->method, self::KNOWN_METHODS, true) ? $request->method : 'OTHER';
        $status = $outcome->response->status;

        if ($this->services !== null) {
            try {
                (new RequestMetrics($this->services->valkey()))->record($outcome->route, $method, $status, $outcome->durationSeconds);
            } catch (\Throwable $e) {
                $this->logger->warn('cannot record request metrics', ['request_id' => $request->requestId, 'error' => $e->getMessage()]);
            }
        }

        $level = match (true) {
            $status >= 500 => 'error',
            $status < 400 && in_array($outcome->route, self::QUIET_ROUTES, true) => 'debug',
            default => 'info',
        };
        $this->logger->log($level, 'request', [
            'method' => $method,
            'route' => $outcome->route,
            'path' => substr($request->path, 0, 256),
            'status' => $status,
            'duration_ms' => round($outcome->durationSeconds * 1000, 2),
            'request_id' => $request->requestId,
        ]);
    }

    private function dispatch(Request $request, RouteMatch $match): Response
    {
        if (str_starts_with($request->path, '/api/')) {
            $this->injectFaults($request);
        }
        if ($match->handler === null) {
            throw $match->allowedMethods !== []
                ? HttpException::methodNotAllowed($match->allowedMethods)
                : HttpException::notFound();
        }
        if (in_array($request->method, self::WRITE_METHODS, true)) {
            $this->authorize($request);
        }

        return ($match->handler)($request, $match->params);
    }

    private function authorize(Request $request): void
    {
        $token = $request->bearerToken();
        if ($token === null || !hash_equals($this->config?->apiToken() ?? '', $token)) {
            throw HttpException::unauthorized();
        }
    }

    /** PULSE_FAULT_LATENCY_MS / PULSE_FAULT_ERROR_RATE for /api/*, only with PULSE_FAULTS_ENABLED=1. */
    private function injectFaults(Request $request): void
    {
        $config = $this->config;
        if ($config === null || !$config->faultsEnabled) {
            return;
        }
        if ($config->faultLatencyMs > 0) {
            usleep($config->faultLatencyMs * 1000);
        }
        if ($config->faultErrorRate > 0 && random_int(1, 1_000_000) <= (int) round($config->faultErrorRate * 1_000_000)) {
            $this->logger->warn('fault injected: error response', ['request_id' => $request->requestId]);

            throw new HttpException(Response::error(500, 'internal'));
        }
    }

    /**
     * Invalid configuration: there is no "failed start" in FPM, so the closest equivalent is
     * liveness OK (restarting won't help), readiness 503 (no traffic) and 500 everywhere else.
     *
     * @return array{0: string, 1: Response}
     */
    private function handleMisconfigured(Request $request): array
    {
        $this->logger->error('invalid configuration', ['error' => $this->configError, 'request_id' => $request->requestId]);

        return match ($request->path) {
            '/healthz' => ['/healthz', HealthController::liveness()],
            '/readyz' => ['/readyz', Response::json(503, ['status' => 'not_ready', 'checks' => ['config' => 'fail']])],
            default => [RouteMatch::UNMATCHED, Response::error(500, 'internal')],
        };
    }

    private function buildRouter(Services $s): Router
    {
        $db = $s->db(...);
        $valkey = $s->valkey(...);
        $monitors = static fn (): MonitorController => new MonitorController(
            new MonitorRepository($s->db()),
            new CheckRepository($s->db()),
        );

        $router = new Router();
        $router->add('GET', '/healthz', static fn (): Response => HealthController::liveness());
        $router->add('GET', '/readyz', static fn (): Response => (new HealthController($db, $valkey, $s->logger))->readiness());
        $router->add('GET', '/metrics', static fn (): Response => (new MetricsController($valkey, $db, $s->logger))->show());

        $router->add('GET', '/api/v1/monitors', static fn (): Response => $monitors()->list());
        $router->add('POST', '/api/v1/monitors', static fn (Request $r): Response => $monitors()->create($r));
        $router->add('GET', '/api/v1/monitors/{id}', static fn (Request $r, array $p): Response => $monitors()->show($p['id']));
        $router->add('PATCH', '/api/v1/monitors/{id}', static fn (Request $r, array $p): Response => $monitors()->update($p['id'], $r));
        $router->add('DELETE', '/api/v1/monitors/{id}', static fn (Request $r, array $p): Response => $monitors()->delete($p['id']));
        $router->add('GET', '/api/v1/monitors/{id}/checks', static fn (Request $r, array $p): Response => $monitors()->checks($p['id'], $r));
        $router->add('GET', '/api/v1/monitors/{id}/uptime', static fn (Request $r, array $p): Response => $monitors()->uptime($p['id'], $r, new UptimeRepository($s->db())));
        $router->add('GET', '/api/v1/incidents', static fn (Request $r): Response => (new IncidentController(new IncidentRepository($s->db())))->list($r));
        $router->add('GET', '/api/v1/status', static fn (): Response => (new StatusController(
            new StatusService($db, $s->valkey(), $s->config->statusCacheTtl, $s->logger),
        ))->show());

        if ($s->config->faultsEnabled) {
            $router->add('GET', '/debug/slow-query', static fn (Request $r): Response => (new DebugController($s->db()))->slowQuery($r));
        }

        return $router;
    }

    private function emit(Request $request, Response $response): void
    {
        http_response_code($response->status);
        foreach ($response->headers as $name => $value) {
            header($name . ': ' . $value);
        }
        if ($response->status !== 204 && $request->method !== 'HEAD') {
            echo $response->body;
        }
    }

    /**
     * Warnings and notices become exceptions (-> 500 + log): a half-broken response is worse than an honest 500.
     * Deprecations are only logged: a PHP upgrade must not turn into an outage. Fatal errors are logged as JSON.
     */
    private function installErrorHandlers(Request $request): void
    {
        $logger = $this->logger;
        set_error_handler(static function (int $severity, string $message, string $file, int $line) use ($logger, $request): bool {
            if ((error_reporting() & $severity) === 0) {
                return false;
            }
            if ($severity === E_DEPRECATED || $severity === E_USER_DEPRECATED) {
                $logger->warn('php deprecation', ['request_id' => $request->requestId, 'error' => $message, 'file' => $file . ':' . $line]);

                return true;
            }

            throw new \ErrorException($message, 0, $severity, $file, $line);
        });

        register_shutdown_function(function () use ($request): void {
            $error = error_get_last();
            if ($error === null || !in_array($error['type'], [E_ERROR, E_PARSE, E_CORE_ERROR, E_COMPILE_ERROR], true)) {
                return;
            }
            $this->logger->error('fatal error', [
                'request_id' => $request->requestId,
                'error' => $error['message'],
                'file' => $error['file'] . ':' . $error['line'],
            ]);
            if (!headers_sent()) {
                http_response_code(500);
                header('Content-Type: application/json');
                echo '{"error":"internal"}';
            }
        });
    }
}
