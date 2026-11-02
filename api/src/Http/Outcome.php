<?php

declare(strict_types=1);

namespace Pulse\Http;

/** Result of Kernel::handle(): the response plus what logs and metrics need to know about it. */
final readonly class Outcome
{
    public function __construct(
        public Response $response,
        public string $route,
        public float $durationSeconds,
    ) {
    }
}
