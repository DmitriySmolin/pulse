<?php

declare(strict_types=1);

namespace Pulse\Validation;

/**
 * Validates monitor bodies for POST (create) and PATCH (partial update).
 * Rules come from CONTRACT.md; the database has the same CHECK constraints as a safety net.
 * Unknown fields are ignored, null is never a valid value.
 */
final class MonitorInput
{
    public const array DEFAULTS = [
        'method' => 'GET',
        'interval_seconds' => 60,
        'timeout_ms' => 5000,
        'expected_status' => 200,
    ];
    public const int MAX_URL_LENGTH = 2048;
    public const int MAX_NAME_LENGTH = 200;

    /** field => [min, max] */
    private const array INT_RANGES = [
        'interval_seconds' => [10, 3600],
        'timeout_ms' => [100, 30000],
        'expected_status' => [100, 599],
    ];

    /**
     * @param array<string, mixed> $data decoded JSON object
     *
     * @return array<string, string|int|bool> normalized values with defaults applied
     */
    public static function forCreate(array $data): array
    {
        $errors = [];
        foreach (['name', 'url'] as $required) {
            if (!array_key_exists($required, $data)) {
                $errors[$required] = 'is required';
            }
        }
        $values = self::validate($data, $errors, allowPaused: false);
        if ($errors !== []) {
            throw new ValidationException($errors);
        }

        return $values + self::DEFAULTS;
    }

    /**
     * @param array<string, mixed> $data decoded JSON object
     *
     * @return array<string, string|int|bool> only the fields that were sent
     */
    public static function forUpdate(array $data): array
    {
        $errors = [];
        $values = self::validate($data, $errors, allowPaused: true);
        if ($errors !== []) {
            throw new ValidationException($errors);
        }

        return $values;
    }

    /**
     * @param array<string, mixed>  $data
     * @param array<string, string> $errors
     *
     * @return array<string, string|int|bool>
     */
    private static function validate(array $data, array &$errors, bool $allowPaused): array
    {
        $out = [];

        if (array_key_exists('name', $data)) {
            $name = $data['name'];
            if (!is_string($name) || trim($name) === '') {
                $errors['name'] = 'must be a non-empty string';
            } elseif ((int) preg_match_all('/./us', trim($name)) > self::MAX_NAME_LENGTH) {
                $errors['name'] = sprintf('must be at most %d characters', self::MAX_NAME_LENGTH);
            } else {
                $out['name'] = trim($name);
            }
        }

        if (array_key_exists('url', $data)) {
            $problem = self::urlProblem($data['url']);
            if ($problem !== null) {
                $errors['url'] = $problem;
            } else {
                $out['url'] = (string) $data['url'];
            }
        }

        foreach (self::INT_RANGES as $field => [$min, $max]) {
            if (!array_key_exists($field, $data)) {
                continue;
            }
            $value = $data[$field];
            if (!is_int($value) || $value < $min || $value > $max) {
                $errors[$field] = sprintf('must be an integer between %d and %d', $min, $max);
            } else {
                $out[$field] = $value;
            }
        }

        if (array_key_exists('method', $data)) {
            $method = is_string($data['method']) ? strtoupper($data['method']) : null;
            if ($method !== 'GET' && $method !== 'HEAD') {
                $errors['method'] = 'must be GET or HEAD';
            } else {
                $out['method'] = $method;
            }
        }

        if ($allowPaused && array_key_exists('paused', $data)) {
            if (!is_bool($data['paused'])) {
                $errors['paused'] = 'must be a boolean';
            } else {
                $out['paused'] = $data['paused'];
            }
        }

        return $out;
    }

    private static function urlProblem(mixed $url): ?string
    {
        if (!is_string($url) || $url === '') {
            return 'must be a non-empty string';
        }
        if (strlen($url) > self::MAX_URL_LENGTH) {
            return sprintf('must be at most %d characters', self::MAX_URL_LENGTH);
        }
        if (preg_match('/[\x00-\x20\x7f]/', $url) === 1) {
            return 'must not contain spaces or control characters';
        }
        $parts = parse_url($url);
        if ($parts === false || !isset($parts['scheme'], $parts['host']) || $parts['host'] === '') {
            return 'must be an absolute http(s) URL';
        }
        if (!in_array(strtolower($parts['scheme']), ['http', 'https'], true)) {
            return 'must use the http or https scheme';
        }

        return null;
    }
}
