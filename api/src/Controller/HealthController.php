<?php

declare(strict_types=1);

namespace Pulse\Controller;

use Closure;
use PDO;
use Predis\ClientInterface;
use Pulse\Http\Response;
use Pulse\Logger;

final readonly class HealthController
{
    /**
     * @param Closure(): PDO             $db
     * @param Closure(): ClientInterface $valkey
     */
    public function __construct(
        private Closure $db,
        private Closure $valkey,
        private Logger $logger,
    ) {
    }

    /** Liveness: the PHP process answers. Never touches dependencies. */
    public static function liveness(): Response
    {
        return Response::json(200, ['status' => 'ok']);
    }

    /** Readiness: PostgreSQL (SELECT 1) and Valkey (PING), each with a short timeout. */
    public function readiness(): Response
    {
        $checks = [
            'postgres' => $this->probe('postgres', fn (): bool => (int) ($this->db)()->query('SELECT 1')->fetchColumn() === 1),
            'valkey' => $this->probe('valkey', fn (): bool => (string) ($this->valkey)()->ping() === 'PONG'),
        ];
        $ready = !in_array('fail', $checks, true);

        return Response::json($ready ? 200 : 503, ['status' => $ready ? 'ready' : 'not_ready', 'checks' => $checks]);
    }

    /** @param Closure(): bool $check */
    private function probe(string $name, Closure $check): string
    {
        try {
            if ($check()) {
                return 'ok';
            }
            $this->logger->warn('readiness check failed', ['check' => $name, 'error' => 'unexpected reply']);
        } catch (\Throwable $e) {
            $this->logger->warn('readiness check failed', ['check' => $name, 'error' => $e->getMessage()]);
        }

        return 'fail';
    }
}
