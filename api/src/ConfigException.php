<?php

declare(strict_types=1);

namespace Pulse;

/**
 * Invalid or missing configuration. For the API this means "the service cannot start":
 * /healthz stays 200, /readyz answers 503 and every other route answers 500.
 */
final class ConfigException extends \RuntimeException
{
    public static function missing(string $name): self
    {
        return new self(sprintf('%s is required (set %1$s or %1$s_FILE)', $name));
    }
}
