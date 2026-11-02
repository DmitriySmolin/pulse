<?php

declare(strict_types=1);

namespace Pulse\Metrics;

/**
 * Prometheus text exposition format (version 0.0.4) rendering. Pure functions, no I/O.
 *
 * @phpstan-type Sample array{labels: array<string, string>, value: int|float}
 * @phpstan-type HistogramSeries array{labels: array<string, string>, buckets: array<string, int>, sum: float, count: int}
 */
final class Prometheus
{
    public const string CONTENT_TYPE = 'text/plain; version=0.0.4; charset=utf-8';

    /** @param list<Sample> $samples */
    public static function counter(string $name, string $help, array $samples): string
    {
        return self::simple($name, 'counter', $help, $samples);
    }

    /** @param list<Sample> $samples */
    public static function gauge(string $name, string $help, array $samples): string
    {
        return self::simple($name, 'gauge', $help, $samples);
    }

    /**
     * @param list<float>           $bounds upper bounds (le) in ascending order, without +Inf
     * @param list<HistogramSeries> $series buckets hold NON-cumulative counts keyed by formatNumber(bound);
     *                                      observations above the last bound are only in `count`
     */
    public static function histogram(string $name, string $help, array $bounds, array $series): string
    {
        $out = self::header($name, 'histogram', $help);
        foreach ($series as $s) {
            $cumulative = 0;
            foreach ($bounds as $bound) {
                $le = self::formatNumber($bound);
                $cumulative += $s['buckets'][$le] ?? 0;
                $out .= $name . '_bucket' . self::labels($s['labels'] + ['le' => $le]) . ' ' . $cumulative . "\n";
            }
            $count = max($s['count'], $cumulative); // +Inf bucket must never be below the last finite one
            $out .= $name . '_bucket' . self::labels($s['labels'] + ['le' => '+Inf']) . ' ' . $count . "\n";
            $out .= $name . '_sum' . self::labels($s['labels']) . ' ' . self::formatNumber($s['sum']) . "\n";
            $out .= $name . '_count' . self::labels($s['labels']) . ' ' . $count . "\n";
        }

        return $out;
    }

    /** @param array<string, string> $labels */
    public static function labels(array $labels): string
    {
        if ($labels === []) {
            return '';
        }
        $pairs = [];
        foreach ($labels as $name => $value) {
            $pairs[] = $name . '="' . self::escapeLabelValue($value) . '"';
        }

        return '{' . implode(',', $pairs) . '}';
    }

    public static function escapeLabelValue(string $value): string
    {
        return str_replace(['\\', '"', "\n"], ['\\\\', '\\"', '\\n'], $value);
    }

    public static function formatNumber(int|float $value): string
    {
        if (is_int($value)) {
            return (string) $value;
        }
        if (is_nan($value)) {
            return 'NaN';
        }
        if (is_infinite($value)) {
            return $value > 0 ? '+Inf' : '-Inf';
        }

        return (string) $value; // shortest round-trip repr: 0.005, 2.5, 10, 1.0E-5
    }

    /** @param list<Sample> $samples */
    private static function simple(string $name, string $type, string $help, array $samples): string
    {
        $out = self::header($name, $type, $help);
        foreach ($samples as $sample) {
            $out .= $name . self::labels($sample['labels']) . ' ' . self::formatNumber($sample['value']) . "\n";
        }

        return $out;
    }

    private static function header(string $name, string $type, string $help): string
    {
        $help = str_replace(['\\', "\n"], ['\\\\', '\\n'], $help);

        return "# HELP {$name} {$help}\n# TYPE {$name} {$type}\n";
    }
}
