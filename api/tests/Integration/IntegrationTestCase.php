<?php

declare(strict_types=1);

namespace Pulse\Tests\Integration;

use PDO;
use PHPUnit\Framework\TestCase;
use Predis\ClientInterface;
use Pulse\Console\Migrator;
use Pulse\Infra\Database;
use Pulse\Infra\Valkey;
use Pulse\Logger;

/**
 * Runs against a real PostgreSQL (TEST_DATABASE_URL) and, where needed, Valkey (TEST_VALKEY_URL).
 * Without these variables the tests are skipped.
 *
 * WARNING: the `public` schema of the test database is dropped before every test.
 * As a guard the database name must contain "test".
 */
abstract class IntegrationTestCase extends TestCase
{
    protected PDO $db;

    /** @var resource */
    protected mixed $log;

    protected function setUp(): void
    {
        $url = getenv('TEST_DATABASE_URL');
        if (!is_string($url) || $url === '') {
            self::markTestSkipped('TEST_DATABASE_URL is not set');
        }
        $dbName = ltrim((string) parse_url($url, PHP_URL_PATH), '/');
        if (!str_contains($dbName, 'test')) {
            self::fail(sprintf('refusing to wipe database "%s": its name must contain "test"', $dbName));
        }

        $log = fopen('php://memory', 'w+b');
        self::assertNotFalse($log);
        $this->log = $log;

        $this->db = Database::connect($url);
        $this->db->exec('DROP SCHEMA public CASCADE; CREATE SCHEMA public');
    }

    protected function databaseUrl(): string
    {
        return (string) getenv('TEST_DATABASE_URL');
    }

    protected function valkeyUrlOrSkip(): string
    {
        $url = getenv('TEST_VALKEY_URL');
        if (!is_string($url) || $url === '') {
            self::markTestSkipped('TEST_VALKEY_URL is not set');
        }

        return $url;
    }

    protected function valkey(): ClientInterface
    {
        return Valkey::connect($this->valkeyUrlOrSkip());
    }

    protected function logger(): Logger
    {
        return new Logger($this->log, 'debug');
    }

    protected function migrate(): void
    {
        (new Migrator($this->db, Migrator::defaultDirectory(), $this->logger()))->migrate();
    }

    /** @return list<array<string, mixed>> decoded JSON log lines */
    protected function logLines(): array
    {
        rewind($this->log);
        $lines = [];
        foreach (explode("\n", trim((string) stream_get_contents($this->log))) as $line) {
            if ($line !== '') {
                $lines[] = json_decode($line, true, 512, JSON_THROW_ON_ERROR);
            }
        }

        return $lines;
    }
}
