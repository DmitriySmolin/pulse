<?php

declare(strict_types=1);

namespace Pulse\Infra;

use PDO;
use Pulse\ConfigException;

/**
 * Turns DATABASE_URL (postgres://user:pass@host:5432/db?sslmode=disable) into a PDO connection.
 */
final class Database
{
    /** libpq connect_timeout (seconds) unless the URL sets its own. Keeps /readyz fast when PG is gone. */
    public const int DEFAULT_CONNECT_TIMEOUT = 2;

    public static function connect(string $url, int $connectTimeout = self::DEFAULT_CONNECT_TIMEOUT): PDO
    {
        ['dsn' => $dsn, 'user' => $user, 'password' => $password] = self::parseUrl($url, $connectTimeout);

        return new PDO($dsn, $user, $password, [
            PDO::ATTR_ERRMODE => PDO::ERRMODE_EXCEPTION,
            PDO::ATTR_DEFAULT_FETCH_MODE => PDO::FETCH_ASSOC,
            PDO::ATTR_EMULATE_PREPARES => false,
        ]);
    }

    /**
     * Query parameters of the URL (sslmode, connect_timeout, application_name, ...) are passed to libpq as is.
     *
     * @return array{dsn: string, user: ?string, password: ?string}
     */
    public static function parseUrl(string $url, int $connectTimeout = self::DEFAULT_CONNECT_TIMEOUT): array
    {
        $parts = parse_url($url);
        if ($parts === false || !isset($parts['scheme'], $parts['host'])) {
            throw new ConfigException('DATABASE_URL is not a valid URL (expected postgres://user:pass@host:5432/db)');
        }
        if (!in_array(strtolower($parts['scheme']), ['postgres', 'postgresql'], true)) {
            throw new ConfigException('DATABASE_URL must start with postgres:// or postgresql://');
        }

        $params = [
            'host' => trim($parts['host'], '[]'),
            'port' => (string) ($parts['port'] ?? 5432),
        ];
        $dbname = rawurldecode(ltrim($parts['path'] ?? '', '/'));
        if ($dbname !== '') {
            $params['dbname'] = $dbname;
        }

        parse_str($parts['query'] ?? '', $query);
        foreach ($query as $key => $value) {
            if (is_string($key) && is_string($value) && preg_match('/^[a-z_]+$/', $key) === 1) {
                $params[$key] = $value;
            }
        }
        $params['connect_timeout'] ??= (string) $connectTimeout;
        $params['application_name'] ??= 'pulse-api';

        $pairs = [];
        foreach ($params as $key => $value) {
            $pairs[] = $key . '=' . self::quote($value);
        }

        return [
            'dsn' => 'pgsql:' . implode(';', $pairs),
            'user' => isset($parts['user']) ? rawurldecode($parts['user']) : null,
            'password' => isset($parts['pass']) ? rawurldecode($parts['pass']) : null,
        ];
    }

    /** libpq conninfo quoting for values with spaces, quotes or backslashes. */
    private static function quote(string $value): string
    {
        if ($value !== '' && preg_match('/[\s\'\\\\;]/', $value) !== 1) {
            return $value;
        }

        return "'" . addcslashes($value, "'\\") . "'";
    }
}
