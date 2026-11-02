<?php

declare(strict_types=1);

namespace Pulse\Http;

final readonly class Response
{
    public const int JSON_FLAGS = JSON_THROW_ON_ERROR | JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE | JSON_PRESERVE_ZERO_FRACTION;

    /** @param array<string, string> $headers */
    public function __construct(
        public int $status,
        public string $body = '',
        public array $headers = [],
    ) {
    }

    /** @param array<string, string> $headers */
    public static function json(int $status, mixed $data, array $headers = []): self
    {
        return self::rawJson($status, json_encode($data, self::JSON_FLAGS), $headers);
    }

    /** @param array<string, string> $headers */
    public static function rawJson(int $status, string $json, array $headers = []): self
    {
        return new self($status, $json, ['Content-Type' => 'application/json'] + $headers);
    }

    /**
     * Error body is always {"error": "<code>", ...}; never put exception details here.
     *
     * @param array<string, mixed> $extra
     */
    public static function error(int $status, string $code, array $extra = []): self
    {
        return self::json($status, ['error' => $code] + $extra);
    }

    public static function noContent(): self
    {
        return new self(204);
    }

    public function withHeader(string $name, string $value): self
    {
        return new self($this->status, $this->body, [$name => $value] + $this->headers);
    }

    public function header(string $name): ?string
    {
        foreach ($this->headers as $key => $value) {
            if (strcasecmp($key, $name) === 0) {
                return $value;
            }
        }

        return null;
    }
}
