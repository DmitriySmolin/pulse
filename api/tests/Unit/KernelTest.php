<?php

declare(strict_types=1);

namespace Pulse\Tests\Unit;

use PHPUnit\Framework\TestCase;
use Pulse\Config;
use Pulse\Http\Kernel;
use Pulse\Http\Request;
use Pulse\Logger;

/** Kernel behaviour that needs no real database: nothing listens on 127.0.0.1:1. */
final class KernelTest extends TestCase
{
    /** @var resource */
    private mixed $log;

    protected function setUp(): void
    {
        $log = fopen('php://memory', 'w+b');
        self::assertNotFalse($log);
        $this->log = $log;
    }

    /** @param array<string, string> $env */
    private function kernel(array $env = []): Kernel
    {
        $config = Config::fromArray($env + [
            'DATABASE_URL' => 'postgres://pulse:secret-password@127.0.0.1:1/pulse?connect_timeout=2',
            'VALKEY_URL' => 'redis://127.0.0.1:1/0',
            'PULSE_API_TOKEN' => 'token',
        ]);

        return new Kernel($config, new Logger($this->log, 'debug'));
    }

    private function logText(): string
    {
        rewind($this->log);

        return (string) stream_get_contents($this->log);
    }

    public function testHealthzNeverTouchesDependencies(): void
    {
        $outcome = $this->kernel()->handle(Request::create('GET', '/healthz'));

        self::assertSame(200, $outcome->response->status);
        self::assertSame('{"status":"ok"}', $outcome->response->body);
        self::assertSame('/healthz', $outcome->route);
    }

    public function testUnknownPathIs404AndUnmatched(): void
    {
        $outcome = $this->kernel()->handle(Request::create('GET', '/api/v1/nope/123'));

        self::assertSame(404, $outcome->response->status);
        self::assertSame('{"error":"not_found"}', $outcome->response->body);
        self::assertSame('unmatched', $outcome->route);
    }

    public function testWriteWithoutTokenIs401BeforeTouchingTheDatabase(): void
    {
        $kernel = $this->kernel();

        foreach ([[], ['Authorization' => 'Bearer wrong']] as $headers) {
            $outcome = $kernel->handle(Request::create('POST', '/api/v1/monitors', $headers, '{"name":"x","url":"http://x/"}'));
            self::assertSame(401, $outcome->response->status);
            self::assertSame('{"error":"unauthorized"}', $outcome->response->body);
        }
    }

    public function testWrongMethodIs405(): void
    {
        $outcome = $this->kernel()->handle(Request::create('PUT', '/api/v1/monitors'));

        self::assertSame(405, $outcome->response->status);
        self::assertSame('GET, POST, HEAD', $outcome->response->header('Allow'));
    }

    public function testUnhandledExceptionIsGeneric500AndDetailsGoOnlyToTheLog(): void
    {
        $outcome = $this->kernel()->handle(Request::create('GET', '/api/v1/monitors', ['X-Request-Id' => 'req-500']));

        self::assertSame(500, $outcome->response->status);
        self::assertSame('{"error":"internal"}', $outcome->response->body);
        self::assertSame('req-500', $outcome->response->header('X-Request-Id'));

        $log = json_decode(trim($this->logText()), true);
        self::assertIsArray($log);
        self::assertSame('error', $log['level']);
        self::assertSame('unhandled exception', $log['msg']);
        self::assertSame('api', $log['service']);
        self::assertSame('req-500', $log['request_id']);
        self::assertSame('PDOException', $log['exception']);
    }

    public function testReadinessFailsFastWhenDependenciesAreDown(): void
    {
        $started = microtime(true);
        $outcome = $this->kernel()->handle(Request::create('GET', '/readyz'));

        self::assertSame(503, $outcome->response->status);
        self::assertSame(['status' => 'not_ready', 'checks' => ['postgres' => 'fail', 'valkey' => 'fail']], json_decode($outcome->response->body, true));
        self::assertLessThan(5.0, microtime(true) - $started);
    }

    public function testFaultEndpointsAre404WhenDisabled(): void
    {
        $outcome = $this->kernel()->handle(Request::create('GET', '/debug/slow-query?seconds=1'));

        self::assertSame(404, $outcome->response->status);
    }

    public function testFaultErrorRateOnlyAffectsApiPaths(): void
    {
        $kernel = $this->kernel(['PULSE_FAULTS_ENABLED' => '1', 'PULSE_FAULT_ERROR_RATE' => '1']);

        self::assertSame(500, $kernel->handle(Request::create('GET', '/api/v1/monitors/1'))->response->status);
        self::assertSame(200, $kernel->handle(Request::create('GET', '/healthz'))->response->status);
    }

    public function testAccessLogLineHasContractFields(): void
    {
        $kernel = $this->kernel();
        $request = Request::create('GET', '/api/v1/nope', ['X-Request-Id' => 'req-log']);
        $kernel->terminate($request, $kernel->handle($request)); // metrics write fails (no Valkey) -> warn, never an exception

        $lines = array_map(static fn (string $l): mixed => json_decode($l, true), explode("\n", trim($this->logText())));
        $access = array_values(array_filter($lines, static fn (mixed $l): bool => is_array($l) && $l['msg'] === 'request'));
        self::assertCount(1, $access);
        foreach (['time', 'level', 'msg', 'service', 'method', 'route', 'status', 'duration_ms', 'request_id'] as $field) {
            self::assertArrayHasKey($field, $access[0]);
        }
        self::assertSame(['GET', 'unmatched', 404, 'req-log'], [$access[0]['method'], $access[0]['route'], $access[0]['status'], $access[0]['request_id']]);
    }

    public function testMisconfiguredApiIsLiveButNotReady(): void
    {
        $kernel = new Kernel(null, new Logger($this->log), 'PULSE_API_TOKEN is required');

        self::assertSame(200, $kernel->handle(Request::create('GET', '/healthz'))->response->status);
        self::assertSame(503, $kernel->handle(Request::create('GET', '/readyz'))->response->status);
        self::assertSame('{"error":"internal"}', $kernel->handle(Request::create('GET', '/api/v1/monitors'))->response->body);
    }
}
