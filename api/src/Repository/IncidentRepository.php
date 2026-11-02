<?php

declare(strict_types=1);

namespace Pulse\Repository;

use PDO;
use Pulse\Support\Time;

final readonly class IncidentRepository
{
    public function __construct(private PDO $db)
    {
    }

    /**
     * @param bool|null $open true = only open, false = only resolved, null = all
     *
     * @return list<array<string, mixed>> newest first
     */
    public function list(?bool $open, int $limit): array
    {
        $where = match ($open) {
            true => 'WHERE i.resolved_at IS NULL',
            false => 'WHERE i.resolved_at IS NOT NULL',
            null => '',
        };
        $stmt = $this->db->prepare(
            "SELECT i.id, i.monitor_id, m.name AS monitor_name, i.started_at, i.resolved_at, i.cause
               FROM incidents i
               JOIN monitors m ON m.id = i.monitor_id
               {$where}
              ORDER BY i.started_at DESC, i.id DESC
              LIMIT :limit",
        );
        $stmt->bindValue(':limit', $limit, PDO::PARAM_INT);
        $stmt->execute();

        return array_map(static fn (array $r): array => [
            'id' => (int) $r['id'],
            'monitor_id' => (int) $r['monitor_id'],
            'monitor_name' => (string) $r['monitor_name'],
            'started_at' => Time::iso($r['started_at']),
            'resolved_at' => Time::iso($r['resolved_at']),
            'cause' => $r['cause'],
        ], $stmt->fetchAll());
    }

    public function countOpen(): int
    {
        return (int) $this->db->query('SELECT count(*) FROM incidents WHERE resolved_at IS NULL')->fetchColumn();
    }
}
