<?php

declare(strict_types=1);

namespace Pulse\Validation;

/** Rendered by the HTTP kernel as 422 {"error":"validation","fields":{...}}. */
final class ValidationException extends \RuntimeException
{
    /** @param array<string, string> $fields field name => human readable problem */
    public function __construct(public readonly array $fields)
    {
        $parts = [];
        foreach ($fields as $field => $problem) {
            $parts[] = $field . ' ' . $problem;
        }
        parent::__construct('validation failed: ' . implode('; ', $parts));
    }
}
