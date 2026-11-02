<?php

declare(strict_types=1);

namespace Pulse\Repository;

use PDO;
use Pulse\Support\Time;

final readonly class MonitorRepository
{
    private const string COLUMNS = 'id, name, url, method, interval_seconds, timeout_ms, expected_status, '
        . 'status, paused, consecutive_failures, last_checked_at, next_check_at, created_at';

    /** Columns the API is allowed to write; status & co. belong to checker. */
    private const array WRITABLE = ['name', 'url', 'method', 'interval_seconds', 'timeout_ms', 'expected_status', 'paused'];

    public function __construct(private PDO $db)
    {
    }

    /** @return list<array<string, mixed>> */
    public function all(): array
    {
        $rows = $this->db->query('SELECT ' . self::COLUMNS . ' FROM monitors ORDER BY id')->fetchAll();

        return array_map(self::toApi(...), $rows);
    }

    /** @return array<string, mixed>|null */
    public function find(int $id): ?array
    {
        $stmt = $this->db->prepare('SELECT ' . self::COLUMNS . ' FROM monitors WHERE id = :id');
        $stmt->execute(['id' => $id]);
        $row = $stmt->fetch();

        return is_array($row) ? self::toApi($row) : null;
    }

    public function exists(int $id): bool
    {
        $stmt = $this->db->prepare('SELECT 1 FROM monitors WHERE id = :id');
        $stmt->execute(['id' => $id]);

        return $stmt->fetchColumn() !== false;
    }

    /**
     * @param array<string, string|int|bool> $values validated by MonitorInput::forCreate()
     *
     * @return array<string, mixed>
     */
    public function create(array $values): array
    {
        $values = array_intersect_key($values, array_flip(self::WRITABLE));
        $columns = array_keys($values);
        $sql = sprintf(
            'INSERT INTO monitors (%s) VALUES (%s) RETURNING %s',
            implode(', ', $columns),
            implode(', ', array_map(static fn (string $c): string => ':' . $c, $columns)),
            self::COLUMNS,
        );
        $stmt = $this->db->prepare($sql);
        self::bind($stmt, $values);
        $stmt->execute();

        return self::toApi($stmt->fetch());
    }

    /**
     * @param array<string, string|int|bool> $changes validated by MonitorInput::forUpdate()
     *
     * @return array<string, mixed>|null null when the monitor does not exist
     */
    public function update(int $id, array $changes): ?array
    {
        $changes = array_intersect_key($changes, array_flip(self::WRITABLE));
        if ($changes === []) {
            return $this->find($id);
        }

        $sets = array_map(static fn (string $c): string => $c . ' = :' . $c, array_keys($changes));
        if (($changes['paused'] ?? null) === false) {
            $sets[] = 'next_check_at = now()'; // a resumed monitor is checked right away
        }

        $stmt = $this->db->prepare(sprintf(
            'UPDATE monitors SET %s WHERE id = :id RETURNING %s',
            implode(', ', $sets),
            self::COLUMNS,
        ));
        self::bind($stmt, $changes + ['id' => $id]);
        $stmt->execute();
        $row = $stmt->fetch();

        return is_array($row) ? self::toApi($row) : null;
    }

    public function delete(int $id): bool
    {
        $stmt = $this->db->prepare('DELETE FROM monitors WHERE id = :id');
        $stmt->execute(['id' => $id]);

        return $stmt->rowCount() > 0;
    }

    /** @param array<string, string|int|bool> $values */
    private static function bind(\PDOStatement $stmt, array $values): void
    {
        foreach ($values as $name => $value) {
            $type = match (true) {
                is_bool($value) => PDO::PARAM_BOOL,
                is_int($value) => PDO::PARAM_INT,
                default => PDO::PARAM_STR,
            };
            $stmt->bindValue(':' . $name, $value, $type);
        }
    }

    /**
     * @param array<string, mixed> $row
     *
     * @return array<string, mixed>
     */
    public static function toApi(array $row): array
    {
        return [
            'id' => (int) $row['id'],
            'name' => (string) $row['name'],
            'url' => (string) $row['url'],
            'method' => (string) $row['method'],
            'interval_seconds' => (int) $row['interval_seconds'],
            'timeout_ms' => (int) $row['timeout_ms'],
            'expected_status' => (int) $row['expected_status'],
            'status' => (string) $row['status'],
            'paused' => self::bool($row['paused']),
            'consecutive_failures' => (int) $row['consecutive_failures'],
            'last_checked_at' => Time::iso($row['last_checked_at']),
            'next_check_at' => Time::iso($row['next_check_at']),
            'created_at' => Time::iso($row['created_at']),
        ];
    }

    /** pdo_pgsql returns native bools, but be tolerant to 't'/'f'. */
    public static function bool(mixed $value): bool
    {
        return $value === true || $value === 't' || $value === 1 || $value === '1';
    }
}
