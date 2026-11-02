<?php

declare(strict_types=1);

namespace Pulse;

/**
 * Structured JSON logger: one line per event with time, level, msg, service + context fields.
 *
 * The web app writes to php://stderr: PHP-FPM forwards worker stderr to its own log
 * (catch_workers_output = yes, decorate_workers_output = no), worker stdout is discarded.
 */
final readonly class Logger
{
    private const array SEVERITY = ['debug' => 10, 'info' => 20, 'warn' => 30, 'error' => 40];

    /**
     * @param resource      $stream      destination for all events
     * @param resource|null $errorStream optional separate destination for warn/error (console: stderr)
     */
    public function __construct(
        private mixed $stream,
        private string $level = 'info',
        private string $service = 'api',
        private mixed $errorStream = null,
    ) {
    }

    public static function stderr(string $level = 'info'): self
    {
        return new self(self::open('php://stderr'), $level);
    }

    /** Console flavour: info/debug to stdout, warn/error to stderr. */
    public static function console(string $level = 'info'): self
    {
        return new self(self::open('php://stdout'), $level, 'api', self::open('php://stderr'));
    }

    public function withLevel(string $level): self
    {
        return new self($this->stream, $level, $this->service, $this->errorStream);
    }

    /** @param array<string, mixed> $context */
    public function log(string $level, string $msg, array $context = []): void
    {
        $severity = self::SEVERITY[$level] ?? self::SEVERITY['info'];
        if ($severity < (self::SEVERITY[$this->level] ?? self::SEVERITY['info'])) {
            return;
        }

        $record = [
            'time' => (new \DateTimeImmutable('now', new \DateTimeZone('UTC')))->format('Y-m-d\TH:i:s.v\Z'),
            'level' => $level,
            'msg' => $msg,
            'service' => $this->service,
        ] + $context;

        $line = json_encode(
            $record,
            JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE | JSON_INVALID_UTF8_SUBSTITUTE | JSON_PARTIAL_OUTPUT_ON_ERROR,
        );
        $stream = $severity >= self::SEVERITY['warn'] && $this->errorStream !== null ? $this->errorStream : $this->stream;
        fwrite($stream, $line . "\n");
    }

    /** @param array<string, mixed> $context */
    public function debug(string $msg, array $context = []): void
    {
        $this->log('debug', $msg, $context);
    }

    /** @param array<string, mixed> $context */
    public function info(string $msg, array $context = []): void
    {
        $this->log('info', $msg, $context);
    }

    /** @param array<string, mixed> $context */
    public function warn(string $msg, array $context = []): void
    {
        $this->log('warn', $msg, $context);
    }

    /** @param array<string, mixed> $context */
    public function error(string $msg, array $context = []): void
    {
        $this->log('error', $msg, $context);
    }

    /** @return array<string, mixed> exception details for the log (never sent to clients) */
    public static function exceptionContext(\Throwable $e): array
    {
        return [
            'error' => $e->getMessage(),
            'exception' => $e::class,
            'file' => $e->getFile() . ':' . $e->getLine(),
            'trace' => substr($e->getTraceAsString(), 0, 4000),
        ];
    }

    /** @return resource */
    private static function open(string $uri): mixed
    {
        $stream = fopen($uri, 'wb');
        if ($stream === false) {
            throw new \RuntimeException('cannot open ' . $uri);
        }

        return $stream;
    }
}
