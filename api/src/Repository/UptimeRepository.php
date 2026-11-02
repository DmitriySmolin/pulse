<?php

declare(strict_types=1);

namespace Pulse\Repository;

use PDO;
use Pulse\Support\Time;

/**
 * Hourly uptime aggregates written by `aggregator` (Kafka consumer) into uptime_hourly.
 * The API only reads them; without Kafka the table is empty and so is the answer.
 */
final readonly class UptimeRepository
{
    public const int MAX_DAYS = 90;

    public function __construct(private PDO $db)
    {
    }

    /** @return list<array{hour: ?string, checks: int, ok: int, uptime: ?float, latency_avg_ms: ?float, latency_max_ms: int}> oldest first */
    public function hourly(int $monitorId, int $days): array
    {
        $stmt = $this->db->prepare(
            'SELECT hour, checks, ok, latency_sum_ms, latency_max_ms
               FROM uptime_hourly
              WHERE monitor_id = :monitor_id
                AND hour >= date_trunc(\'hour\', now()) - make_interval(days => :days)
              ORDER BY hour',
        );
        $stmt->bindValue(':monitor_id', $monitorId, PDO::PARAM_INT);
        $stmt->bindValue(':days', $days, PDO::PARAM_INT);
        $stmt->execute();

        return array_map(self::toApi(...), $stmt->fetchAll());
    }

    /**
     * One uptime_hourly row -> API item. uptime = ok / checks (0..1), average latency = sum / checks.
     * An hour without checks (should not exist, but the DEFAULT 0 allows it) gives nulls, not a division by zero.
     *
     * @param array<string, mixed> $row
     *
     * @return array{hour: ?string, checks: int, ok: int, uptime: ?float, latency_avg_ms: ?float, latency_max_ms: int}
     */
    public static function toApi(array $row): array
    {
        $checks = (int) $row['checks'];
        $ok = (int) $row['ok'];

        return [
            'hour' => Time::iso(is_string($row['hour']) ? $row['hour'] : null),
            'checks' => $checks,
            'ok' => $ok,
            'uptime' => $checks > 0 ? round($ok / $checks, 4) : null,
            'latency_avg_ms' => $checks > 0 ? round((int) $row['latency_sum_ms'] / $checks, 1) : null,
            'latency_max_ms' => (int) $row['latency_max_ms'],
        ];
    }

    /** Deletes aggregated_checks (consumer idempotency keys) older than $days, in batches. */
    public function pruneAggregatedOlderThan(int $days, int $batchSize = 5000): int
    {
        $exists = $this->db->query("SELECT to_regclass('aggregated_checks') IS NOT NULL")->fetchColumn();
        if ($exists !== true && $exists !== 't') {
            return 0; // 0002_kafka is not applied yet
        }

        $stmt = $this->db->prepare(
            'DELETE FROM aggregated_checks
              WHERE check_id IN (SELECT check_id FROM aggregated_checks
                                  WHERE aggregated_at < now() - make_interval(days => :days)
                                  LIMIT :batch)',
        );
        $stmt->bindValue(':days', $days, PDO::PARAM_INT);
        $stmt->bindValue(':batch', $batchSize, PDO::PARAM_INT);

        $total = 0;
        do {
            $stmt->execute();
            $deleted = $stmt->rowCount();
            $total += $deleted;
        } while ($deleted === $batchSize);

        return $total;
    }
}
