<?php

declare(strict_types=1);

namespace Pulse\Console;

use PDO;
use Pulse\Config;
use Pulse\Infra\Database;
use Pulse\Logger;
use Pulse\Repository\CheckRepository;
use Pulse\Repository\UptimeRepository;
use Pulse\Validation\MonitorInput;

/**
 * bin/console. Exit code 0 = success; otherwise != 0 and the reason goes to stderr
 * (1 = runtime error, 2 = wrong usage). Events are JSON lines like in the web app.
 */
final class Application
{
    private const string USAGE = <<<'TXT'
        Usage: php bin/console <command> [options]

        Commands:
          migrate               apply pending migrations/*.sql (idempotent, safe to run in parallel)
          migrate:status        list applied and pending migrations
          seed                  create demo monitors on SEED_TARGET_URL (default http://target:8090)
          prune [--days=30]     delete checks and aggregated_checks older than N days
          wait-db [--timeout=60]  wait until PostgreSQL accepts connections

        Environment: DATABASE_URL (or DATABASE_URL_FILE), LOG_LEVEL, SEED_TARGET_URL.

        TXT;

    /** command => allowed options */
    private const array COMMANDS = [
        'migrate' => [],
        'migrate:status' => [],
        'seed' => [],
        'prune' => ['days'],
        'wait-db' => ['timeout'],
    ];

    /** @param resource $stdout */
    private function __construct(
        private readonly Config $config,
        private readonly Logger $logger,
        private readonly mixed $stdout,
    ) {
    }

    /** @param list<string> $argv */
    public static function main(array $argv): int
    {
        $stdout = fopen('php://stdout', 'wb');
        $stderr = fopen('php://stderr', 'wb');
        assert($stdout !== false && $stderr !== false);

        $command = $argv[1] ?? null;
        if ($command === null || in_array($command, ['help', '--help', '-h'], true)) {
            fwrite($command === null ? $stderr : $stdout, self::USAGE);

            return $command === null ? 2 : 0;
        }
        if (!array_key_exists($command, self::COMMANDS)) {
            fwrite($stderr, "unknown command: {$command}\n\n" . self::USAGE);

            return 2;
        }

        $logger = Logger::console();
        try {
            $options = self::parseOptions(array_slice($argv, 2), self::COMMANDS[$command]);
            $days = self::intOption($options, 'days', 30, 1);
            $timeout = self::intOption($options, 'timeout', 60, 1);
        } catch (\InvalidArgumentException $e) {
            fwrite($stderr, $e->getMessage() . "\n\n" . self::USAGE);

            return 2;
        }

        try {
            $config = Config::fromEnvironment();
            $logger = $logger->withLevel($config->logLevel);
            $app = new self($config, $logger, $stdout);

            return match ($command) {
                'migrate' => $app->migrate(),
                'migrate:status' => $app->migrateStatus(),
                'seed' => $app->seed(),
                'prune' => $app->prune($days),
                'wait-db' => $app->waitDb($timeout),
            };
        } catch (\Throwable $e) {
            $logger->error($e->getMessage(), ['command' => $command, 'exception' => $e::class]);

            return 1;
        }
    }

    private function migrate(): int
    {
        $migrator = new Migrator($this->connect(), Migrator::defaultDirectory(), $this->logger);
        $done = $migrator->migrate();
        $this->logger->info($done === [] ? 'database is up to date' : 'migrations finished', ['applied' => $done]);

        return 0;
    }

    private function migrateStatus(): int
    {
        $migrator = new Migrator($this->connect(), Migrator::defaultDirectory(), $this->logger);
        $files = $migrator->files();
        $applied = $migrator->applied();

        $rows = [];
        foreach ($files as $version => $_) {
            $rows[] = [$version, isset($applied[$version]) ? 'applied' : 'pending', $applied[$version] ?? ''];
        }
        foreach (array_diff_key($applied, $files) as $version => $at) {
            $rows[] = [$version, 'applied (file missing)', $at];
        }

        $out = sprintf("%-32s %-24s %s\n", 'VERSION', 'STATUS', 'APPLIED AT');
        foreach ($rows as [$version, $status, $at]) {
            $out .= sprintf("%-32s %-24s %s\n", $version, $status, $at);
        }
        $pending = count(array_diff_key($files, $applied));
        $out .= sprintf("\napplied: %d, pending: %d\n", count($applied), $pending);
        fwrite($this->stdout, $out);

        return 0;
    }

    private function seed(): int
    {
        $target = rtrim($this->config->seedTargetUrl, '/');
        $demo = [
            ['name' => 'target', 'url' => $target . '/'],
            ['name' => 'target-slow', 'url' => $target . '/delay/300'],
            ['name' => 'target-flaky', 'url' => $target . '/flaky?rate=0.2'],
        ];

        $db = $this->connect();
        // Idempotent: a monitor with the same URL is never created twice.
        $insert = $db->prepare(
            'INSERT INTO monitors (name, url, method, interval_seconds, timeout_ms, expected_status)
             SELECT CAST(:name AS text), CAST(:url AS text), CAST(:method AS text),
                    CAST(:interval_seconds AS int), CAST(:timeout_ms AS int), CAST(:expected_status AS int)
              WHERE NOT EXISTS (SELECT 1 FROM monitors WHERE url = :same_url)',
        );
        foreach ($demo as $monitor) {
            $values = MonitorInput::forCreate($monitor);
            $insert->execute([
                'name' => $values['name'],
                'url' => $values['url'],
                'method' => $values['method'],
                'interval_seconds' => $values['interval_seconds'],
                'timeout_ms' => $values['timeout_ms'],
                'expected_status' => $values['expected_status'],
                'same_url' => $values['url'],
            ]);
            $created = $insert->rowCount() > 0;
            $this->logger->info($created ? 'demo monitor created' : 'demo monitor already exists', [
                'name' => $values['name'],
                'url' => $values['url'],
            ]);
        }

        return 0;
    }

    private function prune(int $days): int
    {
        $db = $this->connect();
        $deleted = (new CheckRepository($db))->pruneOlderThan($days);
        $deletedAggregated = (new UptimeRepository($db))->pruneAggregatedOlderThan($days);
        $this->logger->info('old checks pruned', [
            'days' => $days,
            'deleted' => $deleted,
            'deleted_aggregated_checks' => $deletedAggregated,
        ]);

        return 0;
    }

    private function waitDb(int $timeoutSeconds): int
    {
        $deadline = microtime(true) + $timeoutSeconds;
        $delay = 0.5;
        for ($attempt = 1; ; $attempt++) {
            try {
                $this->connect()->query('SELECT 1');
                $this->logger->info('database is ready', ['attempt' => $attempt]);

                return 0;
            } catch (\PDOException $e) {
                $left = $deadline - microtime(true);
                if ($left <= 0) {
                    $this->logger->error('database is not reachable, giving up', [
                        'timeout_seconds' => $timeoutSeconds,
                        'attempts' => $attempt,
                        'error' => $e->getMessage(),
                    ]);

                    return 1;
                }
                $this->logger->warn('database is not ready yet, retrying', [
                    'attempt' => $attempt,
                    'retry_in_seconds' => round(min($delay, $left), 2),
                    'error' => $e->getMessage(),
                ]);
                usleep((int) (min($delay, $left) * 1_000_000));
                $delay = min($delay * 2, 5.0);
            }
        }
    }

    private function connect(): PDO
    {
        return Database::connect($this->config->databaseUrl());
    }

    /**
     * Accepts --name=value and --name value.
     *
     * @param list<string> $args
     * @param list<string> $allowed
     *
     * @return array<string, string>
     */
    private static function parseOptions(array $args, array $allowed): array
    {
        $options = [];
        for ($i = 0, $n = count($args); $i < $n; $i++) {
            if (preg_match('/^--([a-z-]+)(?:=(.*))?$/', $args[$i], $m) !== 1) {
                throw new \InvalidArgumentException('unexpected argument: ' . $args[$i]);
            }
            $name = $m[1];
            if (!in_array($name, $allowed, true)) {
                throw new \InvalidArgumentException('unknown option: --' . $name);
            }
            if (isset($m[2])) {
                $options[$name] = $m[2];
            } elseif (isset($args[$i + 1])) {
                $options[$name] = $args[++$i];
            } else {
                throw new \InvalidArgumentException("option --{$name} needs a value");
            }
        }

        return $options;
    }

    /** @param array<string, string> $options */
    private static function intOption(array $options, string $name, int $default, int $min): int
    {
        if (!isset($options[$name])) {
            return $default;
        }
        $value = filter_var($options[$name], FILTER_VALIDATE_INT);
        if ($value === false || $value < $min) {
            throw new \InvalidArgumentException(sprintf('--%s must be an integer >= %d', $name, $min));
        }

        return $value;
    }
}
