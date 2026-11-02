<?php

declare(strict_types=1);

namespace Pulse\Tests\Integration;

use Pulse\Console\Migrator;
use Pulse\Repository\CheckRepository;
use Pulse\Repository\UptimeRepository;

final class MigratorTest extends IntegrationTestCase
{
    public function testAppliesAllMigrationsOnceAndIsIdempotent(): void
    {
        $migrator = new Migrator($this->db, Migrator::defaultDirectory(), $this->logger());

        self::assertSame([], $migrator->applied());
        self::assertSame(['0001_init', '0002_kafka'], $migrator->migrate());
        self::assertSame([], $migrator->migrate(), 'second run must be a no-op');
        self::assertSame(['0001_init', '0002_kafka'], array_keys($migrator->applied()));

        $tables = $this->db->query(
            "SELECT string_agg(table_name, ',' ORDER BY table_name) FROM information_schema.tables WHERE table_schema = 'public'",
        )->fetchColumn();
        self::assertSame('aggregated_checks,checks,incidents,monitors,outbox,schema_migrations,uptime_hourly', $tables);
    }

    public function testFailedMigrationLeavesNoTrace(): void
    {
        $dir = sys_get_temp_dir() . '/pulse-migrations-' . bin2hex(random_bytes(4));
        mkdir($dir);
        file_put_contents($dir . '/0001_ok.sql', 'CREATE TABLE schema_migrations (version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());');
        file_put_contents($dir . '/0002_broken.sql', 'CREATE TABLE half_done (id int); SELECT no_such_function();');

        try {
            $migrator = new Migrator($this->db, $dir, $this->logger());
            try {
                $migrator->migrate();
                self::fail('broken migration must throw');
            } catch (\RuntimeException $e) {
                self::assertStringContainsString('migration 0002_broken failed', $e->getMessage());
            }

            self::assertSame(['0001_ok'], array_keys($migrator->applied()));
            self::assertNull($this->db->query("SELECT to_regclass('half_done')")->fetchColumn());
            // the advisory lock was released: another session can take it immediately
            self::assertTrue($this->db->query('SELECT pg_try_advisory_lock(' . Migrator::LOCK_KEY . ')')->fetchColumn());
            $this->db->query('SELECT pg_advisory_unlock(' . Migrator::LOCK_KEY . ')');
        } finally {
            array_map(unlink(...), glob($dir . '/*.sql') ?: []);
            rmdir($dir);
        }
    }

    public function testPruneDeletesOldChecksAndAggregatedChecks(): void
    {
        $this->migrate();
        $id = (int) $this->db->query("INSERT INTO monitors (name, url) VALUES ('m', 'http://t/') RETURNING id")->fetchColumn();
        $this->db->exec("INSERT INTO checks (monitor_id, checked_at, ok) VALUES
            ({$id}, now() - interval '40 days', true), ({$id}, now() - interval '31 days', false), ({$id}, now() - interval '1 day', true)");
        $this->db->exec("INSERT INTO aggregated_checks (check_id, aggregated_at) VALUES
            (1, now() - interval '40 days'), (2, now() - interval '31 days'), (3, now())");

        self::assertSame(2, (new CheckRepository($this->db))->pruneOlderThan(30, batchSize: 1));
        self::assertSame(2, (new UptimeRepository($this->db))->pruneAggregatedOlderThan(30, batchSize: 1));
        self::assertSame(1, (int) $this->db->query('SELECT count(*) FROM checks')->fetchColumn());
        self::assertSame([3], array_map(intval(...), $this->db->query('SELECT check_id FROM aggregated_checks')->fetchAll(\PDO::FETCH_COLUMN)));
    }
}
