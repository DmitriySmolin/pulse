<?php

declare(strict_types=1);

namespace Pulse\Http;

use Pulse\Validation\ValidationException;

final readonly class Request
{
    /** Correlation id: the incoming X-Request-Id when it looks sane, otherwise a fresh UUIDv4. */
    public string $requestId;

    /** hrtime(true) when the request object was created; used for duration metrics. */
    public int $startedAt;

    /**
     * @param array<array-key, mixed> $query
     * @param array<string, string>   $headers lower-case header names
     */
    public function __construct(
        public string $method,
        public string $path,
        public array $query = [],
        public array $headers = [],
        public string $body = '',
    ) {
        $this->startedAt = hrtime(true);
        $incoming = $headers['x-request-id'] ?? '';
        $this->requestId = preg_match('/^[A-Za-z0-9._:-]{1,128}$/', $incoming) === 1 ? $incoming : self::uuid4();
    }

    public static function fromGlobals(): self
    {
        $headers = [];
        foreach ($_SERVER as $key => $value) {
            if (!is_string($key) || !is_string($value)) {
                continue;
            }
            if (str_starts_with($key, 'HTTP_')) {
                $headers[strtolower(str_replace('_', '-', substr($key, 5)))] = $value;
            } elseif ($key === 'CONTENT_TYPE' || $key === 'CONTENT_LENGTH') {
                $headers[strtolower(str_replace('_', '-', $key))] = $value;
            }
        }

        $uri = is_string($_SERVER['REQUEST_URI'] ?? null) ? $_SERVER['REQUEST_URI'] : '/';
        $method = is_string($_SERVER['REQUEST_METHOD'] ?? null) ? $_SERVER['REQUEST_METHOD'] : 'GET';

        return new self(strtoupper($method), self::pathOf($uri), $_GET, $headers, (string) file_get_contents('php://input'));
    }

    /**
     * Convenience constructor for tests: Request::create('GET', '/api/v1/monitors?limit=5').
     *
     * @param array<string, string> $headers
     */
    public static function create(string $method, string $uri, array $headers = [], string $body = ''): self
    {
        $queryString = parse_url('http://localhost' . $uri, PHP_URL_QUERY);
        parse_str(is_string($queryString) ? $queryString : '', $query);

        return new self(strtoupper($method), self::pathOf($uri), $query, array_change_key_case($headers), $body);
    }

    public function header(string $name): ?string
    {
        return $this->headers[strtolower($name)] ?? null;
    }

    public function bearerToken(): ?string
    {
        $header = $this->header('authorization');
        if ($header === null || preg_match('/^Bearer\s+(\S+)\s*$/i', $header, $m) !== 1) {
            return null;
        }

        return $m[1];
    }

    public function queryString(string $name): ?string
    {
        $value = $this->query[$name] ?? null;

        return is_string($value) ? $value : null;
    }

    /** Integer query parameter in [$min, $max]; invalid values are a 422, not a silent clamp. */
    public function queryInt(string $name, int $default, int $min, int $max): int
    {
        $raw = $this->query[$name] ?? null;
        if ($raw === null || $raw === '') {
            return $default;
        }
        $value = is_string($raw) ? filter_var($raw, FILTER_VALIDATE_INT) : false;
        if ($value === false || $value < $min || $value > $max) {
            throw new ValidationException([$name => sprintf('must be an integer between %d and %d', $min, $max)]);
        }

        return $value;
    }

    /**
     * Decodes the body as a JSON object.
     *
     * @return array<string, mixed>
     */
    public function jsonObject(): array
    {
        try {
            $data = json_decode($this->body, false, 32, JSON_THROW_ON_ERROR);
        } catch (\JsonException) {
            throw HttpException::badRequest('request body must be valid JSON');
        }
        if (!$data instanceof \stdClass) {
            throw HttpException::badRequest('request body must be a JSON object');
        }

        return get_object_vars($data);
    }

    private static function pathOf(string $uri): string
    {
        $q = strpos($uri, '?');
        $path = $q === false ? $uri : substr($uri, 0, $q);

        return $path === '' ? '/' : $path;
    }

    private static function uuid4(): string
    {
        $b = random_bytes(16);
        $b[6] = chr((ord($b[6]) & 0x0F) | 0x40);
        $b[8] = chr((ord($b[8]) & 0x3F) | 0x80);

        return vsprintf('%s%s-%s-%s-%s-%s%s%s', str_split(bin2hex($b), 4));
    }
}
