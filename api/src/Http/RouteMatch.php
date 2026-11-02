<?php

declare(strict_types=1);

namespace Pulse\Http;

use Closure;

final readonly class RouteMatch
{
    /** Route label for paths that match no template. */
    public const string UNMATCHED = 'unmatched';

    /**
     * @param array<string, int> $params
     * @param list<string>       $allowedMethods non-empty when the path matched but the method did not (405)
     */
    private function __construct(
        public string $route,
        public ?Closure $handler,
        public array $params,
        public array $allowedMethods,
    ) {
    }

    /** @param array<string, int> $params */
    public static function found(string $route, Closure $handler, array $params): self
    {
        return new self($route, $handler, $params, []);
    }

    /** @param list<string> $allowed */
    public static function methodNotAllowed(string $route, array $allowed): self
    {
        return new self($route, null, [], $allowed);
    }

    public static function notFound(): self
    {
        return new self(self::UNMATCHED, null, [], []);
    }
}
