<?php

declare(strict_types=1);

namespace Pulse\Infra;

use Predis\Client;
use Predis\ClientInterface;
use Pulse\ConfigException;

/**
 * Turns VALKEY_URL (redis://[user:pass@]host:6379/0, rediss:// for TLS) into a Predis client.
 * Predis connects lazily, on the first command.
 */
final class Valkey
{
    /** Seconds for connect and for each read/write. Short on purpose: Valkey is a cache, not a source of truth. */
    public const float DEFAULT_TIMEOUT = 1.0;

    public static function connect(string $url, float $timeout = self::DEFAULT_TIMEOUT): ClientInterface
    {
        $params = self::parseUrl($url) + ['timeout' => $timeout, 'read_write_timeout' => $timeout];

        return new Client($params, ['exceptions' => true]);
    }

    /** @return array<string, string|int> */
    public static function parseUrl(string $url): array
    {
        $parts = parse_url($url);
        if ($parts === false || !isset($parts['scheme'], $parts['host'])) {
            throw new ConfigException('VALKEY_URL is not a valid URL (expected redis://host:6379/0)');
        }
        $scheme = match (strtolower($parts['scheme'])) {
            'redis', 'valkey', 'tcp' => 'tcp',
            'rediss', 'valkeys', 'tls' => 'tls',
            default => throw new ConfigException('VALKEY_URL must start with redis:// or rediss://'),
        };

        $params = [
            'scheme' => $scheme,
            'host' => trim($parts['host'], '[]'),
            'port' => $parts['port'] ?? 6379,
        ];

        $db = ltrim($parts['path'] ?? '', '/');
        if ($db !== '') {
            if (!ctype_digit($db)) {
                throw new ConfigException('VALKEY_URL database (path) must be a number, e.g. redis://valkey:6379/0');
            }
            $params['database'] = (int) $db;
        }
        if (isset($parts['user']) && $parts['user'] !== '') {
            $params['username'] = rawurldecode($parts['user']);
        }
        if (isset($parts['pass']) && $parts['pass'] !== '') {
            $params['password'] = rawurldecode($parts['pass']);
        }

        return $params;
    }
}
