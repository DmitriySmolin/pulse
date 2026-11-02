<?php

declare(strict_types=1);

namespace Pulse\Status;

use Closure;
use PDO;
use Predis\ClientInterface;
use Pulse\Http\Response;
use Pulse\Logger;
use Pulse\Repository\IncidentRepository;
use Pulse\Support\Time;

/**
 * Summary for the public status page, cached in Valkey for PULSE_STATUS_CACHE_TTL seconds.
 * The cache protects PostgreSQL from a crowd refreshing the page: on a hit no DB connection is opened.
 * If Valkey is down the summary is computed on every request (slower, but still correct).
 */
final readonly class StatusService
{
    public const string CACHE_KEY = 'pulse:cache:status:v1';

    /** @param Closure(): PDO $db */
    public function __construct(
        private Closure $db,
        private ClientInterface $valkey,
        private int $ttl,
        private Logger $logger,
    ) {
    }

    /** @return array{0: string, 1: bool} JSON body and whether it was served from cache */
    public function json(): array
    {
        if ($this->ttl > 0) {
            try {
                $cached = $this->valkey->get(self::CACHE_KEY);
                if (is_string($cached) && $cached !== '') {
                    return [$cached, true];
                }
            } catch (\Throwable $e) {
                $this->logger->warn('status cache read failed', ['error' => $e->getMessage()]);
            }
        }

        $json = json_encode($this->build(), Response::JSON_FLAGS);

        if ($this->ttl > 0) {
            try {
                $this->valkey->set(self::CACHE_KEY, $json, 'EX', $this->ttl);
            } catch (\Throwable $e) {
                $this->logger->warn('status cache write failed', ['error' => $e->getMessage()]);
            }
        }

        return [$json, false];
    }

    /** @return array{generated_at: string, overall: string, monitors: list<array<string, mixed>>, open_incidents: list<array<string, mixed>>} */
    public function build(): array
    {
        $db = ($this->db)();

        // Paused monitors are not shown on the status page and do not affect `overall`.
        $rows = $db->query(
            "SELECT m.id, m.name, m.status, m.last_checked_at,
                    (SELECT avg(CASE WHEN c.ok THEN 1.0 ELSE 0.0 END)
                       FROM checks c
                      WHERE c.monitor_id = m.id
                        AND c.checked_at > now() - interval '24 hours') AS uptime_24h
               FROM monitors m
              WHERE NOT m.paused
              ORDER BY m.name, m.id",
        )->fetchAll();

        $monitors = array_map(static fn (array $r): array => [
            'id' => (int) $r['id'],
            'name' => (string) $r['name'],
            'status' => (string) $r['status'],
            'uptime_24h' => $r['uptime_24h'] === null ? null : round((float) $r['uptime_24h'], 4),
            'last_checked_at' => Time::iso($r['last_checked_at']),
        ], $rows);

        return [
            'generated_at' => Time::now(),
            'overall' => Overall::compute(array_column($monitors, 'status')),
            'monitors' => $monitors,
            'open_incidents' => (new IncidentRepository($db))->list(open: true, limit: 100),
        ];
    }
}
