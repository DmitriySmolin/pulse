<?php

declare(strict_types=1);

namespace Pulse\Http;

/** Thrown by handlers to stop processing and answer with a ready response (404, 401, ...). */
final class HttpException extends \RuntimeException
{
    public function __construct(public readonly Response $response)
    {
        parent::__construct('HTTP ' . $response->status);
    }

    public static function notFound(): self
    {
        return new self(Response::error(404, 'not_found'));
    }

    public static function badRequest(string $message): self
    {
        return new self(Response::error(400, 'bad_request', ['message' => $message]));
    }

    public static function unauthorized(): self
    {
        return new self(Response::error(401, 'unauthorized')->withHeader('WWW-Authenticate', 'Bearer'));
    }

    /** @param list<string> $allowed */
    public static function methodNotAllowed(array $allowed): self
    {
        return new self(Response::error(405, 'method_not_allowed')->withHeader('Allow', implode(', ', $allowed)));
    }
}
