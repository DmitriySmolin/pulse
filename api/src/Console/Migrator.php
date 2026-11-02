<?php

declare(strict_types=1);

namespace Pulse\Console;

use PDO;
use Pulse\Logger;
use Pulse\Support\Time;

/**
 * Applies migrations/NNNN_name.sql in order. Version = file name without ".sql".
 *
 * - Each migration runs in its own transaction together with its schema_migrations row,
 *   so a failed migration leaves no trace and can be fixed and re-run.
 * - The whole run holds a session-level advisory lock: parallel `migrate` processes
 *   (several replicas, initContainers, a Job retry) queue up, and the late ones find nothing to do.
 */
final readonly class Migrator
{
    /** Arbitrary constant shared by every `migrate` process. */
    public const int LOCK_KEY = 7_857_300;

    public function __construct(
        private PDO $db,
        private string $directory,
        private Logger $logger,
    ) {
    }

    public static function defaultDirectory(): string
    {
        return dirname(__DIR__, 2) . '/migrations';
    }

    /** @return array<string, string> version => file path, sorted by version */
    public function files(): array
    {
        $files = [];
        foreach (glob($this->directory . '/[0-9][0-9][0-9][0-9]_*.sql') ?: [] as $path) {
            $files[basename($path, '.sql')] = $path;
        }
        ksort($files, SORT_STRING);

        return $files;
    }

    /** @return array<string, string> version => applied_at (ISO 8601) */
    public function applied(): array
    {
        $exists = $this->db->query("SELECT to_regclass('schema_migrations') IS NOT NULL")->fetchColumn();
        if ($exists !== true && $exists !== 't') {
            return [];
        }
        $applied = [];
        foreach ($this->db->query('SELECT version, applied_at FROM schema_migrations ORDER BY version') as $row) {
            $applied[(string) $row['version']] = (string) Time::iso($row['applied_at']);
        }

        return $applied;
    }

    /** @return list<string> versions applied by this run */
    public function migrate(): array
    {
        $files = $this->files();
        if ($files === []) {
            throw new \RuntimeException('no migration files found in ' . $this->directory);
        }

        $this->lock();
        try {
            $applied = $this->applied();
            $done = [];
            foreach ($files as $version => $path) {
                if (isset($applied[$version])) {
                    continue;
                }
                $this->apply($version, $path);
                $done[] = $version;
            }

            return $done;
        } finally {
            $this->db->query('SELECT pg_advisory_unlock(' . self::LOCK_KEY . ')');
        }
    }

    private function lock(): void
    {
        $acquired = $this->db->query('SELECT pg_try_advisory_lock(' . self::LOCK_KEY . ')')->fetchColumn();
        if ($acquired === true || $acquired === 't') {
            return;
        }
        $this->logger->info('another migrate is running, waiting for the advisory lock', ['lock_key' => self::LOCK_KEY]);
        $this->db->query('SELECT pg_advisory_lock(' . self::LOCK_KEY . ')');
    }

    private function apply(string $version, string $path): void
    {
        $sql = file_get_contents($path);
        if ($sql === false) {
            throw new \RuntimeException('cannot read ' . $path);
        }

        $started = hrtime(true);
        $this->db->beginTransaction();
        try {
            $this->db->exec($sql);
            $this->db->prepare('INSERT INTO schema_migrations (version) VALUES (:version)')->execute(['version' => $version]);
            $this->db->commit();
        } catch (\Throwable $e) {
            $this->db->rollBack();

            throw new \RuntimeException(sprintf('migration %s failed: %s', $version, $e->getMessage()), 0, $e);
        }

        $this->logger->info('migration applied', [
            'version' => $version,
            'duration_ms' => round((hrtime(true) - $started) / 1e6, 2),
        ]);
    }
}
