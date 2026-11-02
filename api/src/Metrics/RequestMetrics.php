<?php

declare(strict_types=1);

namespace Pulse\Metrics;

use Predis\ClientInterface;

/**
 * HTTP request counters and duration histogram stored in Valkey.
 *
 * PHP-FPM is shared-nothing: each worker (and each pod) is a separate process, so in-memory counters
 * would be lost after every request. All workers increment the same Valkey hashes instead, and
 * /metrics renders them. Two hashes are used:
 *
 *   pulse:metrics:http_requests  field ["route","method","code"]      -> count
 *   pulse:metrics:http_duration  field ["route","bucket","<le>"]      -> observations in that bucket
 *                                field ["route","sum"] / ["route","count"]
 */
final readonly class RequestMetrics
{
    /** Same as the Prometheus client default buckets, in seconds. */
    public const array BUCKETS = [0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0];

    public const string REQUESTS_KEY = 'pulse:metrics:http_requests';
    public const string DURATION_KEY = 'pulse:metrics:http_duration';

    public function __construct(private ClientInterface $valkey)
    {
    }

    public function record(string $route, string $method, int $code, float $seconds): void
    {
        $bucket = self::bucketFor($seconds);

        // MULTI/EXEC: a scrape never sees `count` updated without the matching bucket.
        $this->valkey->transaction(static function ($tx) use ($route, $method, $code, $seconds, $bucket): void {
            $tx->hincrby(self::REQUESTS_KEY, self::field([$route, $method, (string) $code]), 1);
            if ($bucket !== null) {
                $tx->hincrby(self::DURATION_KEY, self::field([$route, 'bucket', $bucket]), 1);
            }
            $tx->hincrbyfloat(self::DURATION_KEY, self::field([$route, 'sum']), $seconds);
            $tx->hincrby(self::DURATION_KEY, self::field([$route, 'count']), 1);
        });
    }

    /** Exposition text for both metric families. */
    public function render(): string
    {
        $requests = $this->valkey->hgetall(self::REQUESTS_KEY);
        $durations = $this->valkey->hgetall(self::DURATION_KEY);

        return Prometheus::counter(
            'pulse_api_http_requests_total',
            'HTTP requests handled by the API.',
            self::requestSamples($requests),
        ) . Prometheus::histogram(
            'pulse_api_http_request_duration_seconds',
            'HTTP request duration in seconds.',
            self::BUCKETS,
            self::durationSeries($durations),
        );
    }

    /** First bucket bound >= $seconds as formatted `le`, or null when it only belongs to +Inf. */
    public static function bucketFor(float $seconds): ?string
    {
        foreach (self::BUCKETS as $bound) {
            if ($seconds <= $bound) {
                return Prometheus::formatNumber($bound);
            }
        }

        return null;
    }

    /**
     * @param array<string, string> $hash
     *
     * @return list<array{labels: array<string, string>, value: int}>
     */
    public static function requestSamples(array $hash): array
    {
        ksort($hash);
        $samples = [];
        foreach ($hash as $field => $value) {
            $key = self::parseField((string) $field);
            if ($key === null || count($key) !== 3) {
                continue;
            }
            $samples[] = ['labels' => ['route' => $key[0], 'method' => $key[1], 'code' => $key[2]], 'value' => (int) $value];
        }

        return $samples;
    }

    /**
     * @param array<string, string> $hash
     *
     * @return list<array{labels: array<string, string>, buckets: array<string, int>, sum: float, count: int}>
     */
    public static function durationSeries(array $hash): array
    {
        $byRoute = [];
        foreach ($hash as $field => $value) {
            $key = self::parseField((string) $field);
            if ($key === null) {
                continue;
            }
            $route = $key[0];
            $byRoute[$route] ??= ['labels' => ['route' => $route], 'buckets' => [], 'sum' => 0.0, 'count' => 0];
            if ($key[1] === 'bucket' && isset($key[2])) {
                $byRoute[$route]['buckets'][$key[2]] = (int) $value;
            } elseif ($key[1] === 'sum') {
                $byRoute[$route]['sum'] = (float) $value;
            } elseif ($key[1] === 'count') {
                $byRoute[$route]['count'] = (int) $value;
            }
        }
        ksort($byRoute);

        return array_values($byRoute);
    }

    /** @param list<string> $parts */
    private static function field(array $parts): string
    {
        return json_encode($parts, JSON_THROW_ON_ERROR | JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE);
    }

    /** @return list<string>|null */
    private static function parseField(string $field): ?array
    {
        $parts = json_decode($field, true);
        if (!is_array($parts) || count($parts) < 2 || !array_is_list($parts)) {
            return null;
        }
        foreach ($parts as $part) {
            if (!is_string($part)) {
                return null;
            }
        }

        return $parts;
    }
}
