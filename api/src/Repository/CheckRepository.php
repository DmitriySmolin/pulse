<?php

declare(strict_types=1);

namespace Pulse\Repository;

use PDO;
use Pulse\Support\Time;

final readonly class CheckRepository
{
    public function __construct(private PDO $db)
    {
    }

    /** @return list<array<string, mixed>> newest first */
    public function forMonitor(int $monitorId, int $limit): array
    {
        $stmt = $this->db->prepare(
            'SELECT id, monitor_id, checked_at, ok, status_code, latency_ms, error, snapshot_key
               FROM checks
              WHERE monitor_id = :monitor_id
              ORDER BY checked_at DESC, id DESC
              LIMIT :limit',
        );
        $stmt->bindValue(':monitor_id', $monitorId, PDO::PARAM_INT);
        $stmt->bindValue(':limit', $limit, PDO::PARAM_INT);
        $stmt->execute();

        return array_map(static fn (array $r): array => [
            'id' => (int) $r['id'],
            'monitor_id' => (int) $r['monitor_id'],
            'checked_at' => Time::iso($r['checked_at']),
            'ok' => MonitorRepository::bool($r['ok']),
            'status_code' => $r['status_code'] === null ? null : (int) $r['status_code'],
            'latency_ms' => $r['latency_ms'] === null ? null : (int) $r['latency_ms'],
            'error' => $r['error'],
            'snapshot_key' => $r['snapshot_key'],
        ], $stmt->fetchAll());
    }

    /** Deletes checks older than $days in batches, so a big prune does not hold one huge lock. */
    public function pruneOlderThan(int $days, int $batchSize = 5000): int
    {
        $stmt = $this->db->prepare(
            'DELETE FROM checks
              WHERE id IN (SELECT id FROM checks
                            WHERE checked_at < now() - make_interval(days => :days)
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
