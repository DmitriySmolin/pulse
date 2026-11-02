<?php

declare(strict_types=1);

namespace Pulse;

/**
 * Configuration from environment variables (see CONTRACT.md, "Переменные окружения").
 *
 * Any variable NAME may instead be given as NAME_FILE: a path to a file holding the value
 * (Docker/Kubernetes secrets). The trailing newline of the file is stripped.
 * Setting both NAME and NAME_FILE is an error. An empty value counts as "not set".
 */
final readonly class Config
{
    public const string DEFAULT_SEED_TARGET_URL = 'http://target:8090';
    public const array LOG_LEVELS = ['debug', 'info', 'warn', 'error'];

    private function __construct(
        private ?string $databaseUrl,
        private ?string $valkeyUrl,
        private ?string $apiToken,
        public int $statusCacheTtl,
        public string $logLevel,
        public bool $faultsEnabled,
        public int $faultLatencyMs,
        public float $faultErrorRate,
        public string $seedTargetUrl,
    ) {
    }

    /** Reads the real process environment (under PHP-FPM this requires `clear_env = no`). */
    public static function fromEnvironment(): self
    {
        return self::fromArray(getenv());
    }

    /**
     * @param array<string, string> $env
     *
     * @throws ConfigException
     */
    public static function fromArray(array $env): self
    {
        $read = static fn (string $name): ?string => self::read($env, $name);

        return new self(
            databaseUrl: $read('DATABASE_URL'),
            valkeyUrl: $read('VALKEY_URL'),
            apiToken: $read('PULSE_API_TOKEN'),
            statusCacheTtl: self::int('PULSE_STATUS_CACHE_TTL', $read('PULSE_STATUS_CACHE_TTL'), 10, 0, 86_400),
            logLevel: self::logLevel($read('LOG_LEVEL')),
            faultsEnabled: self::bool('PULSE_FAULTS_ENABLED', $read('PULSE_FAULTS_ENABLED')),
            faultLatencyMs: self::int('PULSE_FAULT_LATENCY_MS', $read('PULSE_FAULT_LATENCY_MS'), 0, 0, 60_000),
            faultErrorRate: self::float('PULSE_FAULT_ERROR_RATE', $read('PULSE_FAULT_ERROR_RATE'), 0.0, 0.0, 1.0),
            seedTargetUrl: $read('SEED_TARGET_URL') ?? self::DEFAULT_SEED_TARGET_URL,
        );
    }

    /** Checks that everything the HTTP API needs is present. */
    public function assertApiRequirements(): void
    {
        $this->databaseUrl();
        $this->valkeyUrl();
        $this->apiToken();
    }

    public function databaseUrl(): string
    {
        return $this->databaseUrl ?? throw ConfigException::missing('DATABASE_URL');
    }

    public function valkeyUrl(): string
    {
        return $this->valkeyUrl ?? throw ConfigException::missing('VALKEY_URL');
    }

    public function apiToken(): string
    {
        return $this->apiToken ?? throw ConfigException::missing('PULSE_API_TOKEN');
    }

    /** @param array<string, string> $env */
    private static function read(array $env, string $name): ?string
    {
        $direct = self::nonEmpty($env[$name] ?? null);
        $file = self::nonEmpty($env[$name . '_FILE'] ?? null);

        if ($direct !== null && $file !== null) {
            throw new ConfigException(sprintf('both %1$s and %1$s_FILE are set; use only one of them', $name));
        }
        if ($file === null) {
            return $direct;
        }
        if (!is_file($file) || !is_readable($file)) {
            throw new ConfigException(sprintf('%s_FILE points to a missing or unreadable file: %s', $name, $file));
        }
        $content = file_get_contents($file);
        if ($content === false) {
            throw new ConfigException(sprintf('cannot read %s_FILE: %s', $name, $file));
        }
        $value = rtrim($content, "\r\n");
        if ($value === '') {
            throw new ConfigException(sprintf('%s_FILE points to an empty file: %s', $name, $file));
        }

        return $value;
    }

    private static function nonEmpty(?string $value): ?string
    {
        return $value === null || $value === '' ? null : $value;
    }

    private static function int(string $name, ?string $value, int $default, int $min, int $max): int
    {
        if ($value === null) {
            return $default;
        }
        $int = filter_var(trim($value), FILTER_VALIDATE_INT);
        if ($int === false || $int < $min || $int > $max) {
            throw new ConfigException(sprintf('%s must be an integer between %d and %d, got "%s"', $name, $min, $max, $value));
        }

        return $int;
    }

    private static function float(string $name, ?string $value, float $default, float $min, float $max): float
    {
        if ($value === null) {
            return $default;
        }
        $float = filter_var(trim($value), FILTER_VALIDATE_FLOAT);
        if ($float === false || $float < $min || $float > $max) {
            throw new ConfigException(sprintf('%s must be a number between %s and %s, got "%s"', $name, $min, $max, $value));
        }

        return $float;
    }

    private static function bool(string $name, ?string $value): bool
    {
        return match (strtolower(trim($value ?? '0'))) {
            '1', 'true', 'yes', 'on' => true,
            '0', 'false', 'no', 'off' => false,
            default => throw new ConfigException(sprintf('%s must be 0 or 1, got "%s"', $name, (string) $value)),
        };
    }

    private static function logLevel(?string $value): string
    {
        $level = strtolower($value ?? 'info');
        if (!in_array($level, self::LOG_LEVELS, true)) {
            throw new ConfigException(sprintf('LOG_LEVEL must be one of %s, got "%s"', implode('|', self::LOG_LEVELS), (string) $value));
        }

        return $level;
    }
}
