<?php

declare(strict_types=1);

namespace Pulse\Support;

final class Time
{
    /** PostgreSQL timestamptz text ("2026-10-12 10:00:00.123+03") -> "2026-10-12T07:00:00Z". */
    public static function iso(?string $timestamp): ?string
    {
        if ($timestamp === null) {
            return null;
        }

        return (new \DateTimeImmutable($timestamp))
            ->setTimezone(new \DateTimeZone('UTC'))
            ->format('Y-m-d\TH:i:s\Z');
    }

    public static function now(): string
    {
        return (new \DateTimeImmutable('now', new \DateTimeZone('UTC')))->format('Y-m-d\TH:i:s\Z');
    }
}
