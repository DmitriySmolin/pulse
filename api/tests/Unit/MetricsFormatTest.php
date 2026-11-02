<?php

declare(strict_types=1);

namespace Pulse\Tests\Unit;

use PHPUnit\Framework\TestCase;
use Pulse\Metrics\Prometheus;
use Pulse\Metrics\RequestMetrics;

final class MetricsFormatTest extends TestCase
{
    public function testCounterHasHelpTypeAndLabels(): void
    {
        $text = Prometheus::counter('demo_total', 'Demo counter.', [
            ['labels' => ['route' => '/a', 'code' => '200'], 'value' => 3],
        ]);

        self::assertSame(
            "# HELP demo_total Demo counter.\n# TYPE demo_total counter\ndemo_total{route=\"/a\",code=\"200\"} 3\n",
            $text,
        );
    }

    public function testFamilyWithoutSamplesStillHasHeader(): void
    {
        self::assertSame("# HELP g Gauge.\n# TYPE g gauge\n", Prometheus::gauge('g', 'Gauge.', []));
    }

    public function testLabelValuesAreEscaped(): void
    {
        self::assertSame('{v="a\\\\b\\"c\\nd"}', Prometheus::labels(['v' => "a\\b\"c\nd"]));
    }

    public function testHistogramBucketsAreCumulativeAndEndWithInf(): void
    {
        $text = Prometheus::histogram('h_seconds', 'H.', [0.1, 1.0], [[
            'labels' => ['route' => '/x'],
            'buckets' => ['0.1' => 2, '1' => 3],
            'sum' => 4.25,
            'count' => 6, // one observation above the last bound
        ]]);

        self::assertSame(
            "# HELP h_seconds H.\n# TYPE h_seconds histogram\n"
            . "h_seconds_bucket{route=\"/x\",le=\"0.1\"} 2\n"
            . "h_seconds_bucket{route=\"/x\",le=\"1\"} 5\n"
            . "h_seconds_bucket{route=\"/x\",le=\"+Inf\"} 6\n"
            . "h_seconds_sum{route=\"/x\"} 4.25\n"
            . "h_seconds_count{route=\"/x\"} 6\n",
            $text,
        );
    }

    public function testInfBucketNeverBelowLastFiniteBucket(): void
    {
        // count lags behind buckets (e.g. a half-applied write): +Inf and _count must still be monotonic
        $text = Prometheus::histogram('h', 'H.', [1.0], [['labels' => [], 'buckets' => ['1' => 5], 'sum' => 1.0, 'count' => 3]]);

        self::assertStringContainsString("h_bucket{le=\"+Inf\"} 5\nh_sum 1\nh_count 5\n", $text);
    }

    public function testBucketForUsesInclusiveUpperBound(): void
    {
        self::assertSame('0.005', RequestMetrics::bucketFor(0.001));
        self::assertSame('0.005', RequestMetrics::bucketFor(0.005));
        self::assertSame('0.01', RequestMetrics::bucketFor(0.0051));
        self::assertSame('10', RequestMetrics::bucketFor(10.0));
        self::assertNull(RequestMetrics::bucketFor(10.01));
    }

    public function testRequestSamplesFromValkeyHash(): void
    {
        $samples = RequestMetrics::requestSamples([
            '["/healthz","GET","200"]' => '7',
            'garbage' => '1',
            '["unmatched","GET","404"]' => '2',
        ]);

        self::assertSame([
            ['labels' => ['route' => '/healthz', 'method' => 'GET', 'code' => '200'], 'value' => 7],
            ['labels' => ['route' => 'unmatched', 'method' => 'GET', 'code' => '404'], 'value' => 2],
        ], $samples);
    }

    public function testDurationSeriesFromValkeyHash(): void
    {
        $series = RequestMetrics::durationSeries([
            '["/b","count"]' => '1',
            '["/a","bucket","0.005"]' => '2',
            '["/a","sum"]' => '0.006',
            '["/a","count"]' => '2',
            '["/b","sum"]' => '30.5',
        ]);

        self::assertSame([
            ['labels' => ['route' => '/a'], 'buckets' => ['0.005' => 2], 'sum' => 0.006, 'count' => 2],
            ['labels' => ['route' => '/b'], 'buckets' => [], 'sum' => 30.5, 'count' => 1],
        ], $series);
    }

    /** Every line must be a comment or `name{labels} value`, which is what promtool and Prometheus parse. */
    public function testRenderedHistogramLinesAreWellFormed(): void
    {
        $text = Prometheus::histogram(
            'pulse_api_http_request_duration_seconds',
            'HTTP request duration in seconds.',
            RequestMetrics::BUCKETS,
            RequestMetrics::durationSeries(['["/healthz","bucket","0.005"]' => '1', '["/healthz","sum"]' => '0.001', '["/healthz","count"]' => '1']),
        );

        foreach (explode("\n", rtrim($text, "\n")) as $line) {
            self::assertMatchesRegularExpression('/^(# (HELP|TYPE) \S+ .+|[a-z_:][a-z0-9_:]*(\{[^}]*\})? \S+)$/', $line);
        }
        self::assertSame(count(RequestMetrics::BUCKETS) + 1, substr_count($text, '_bucket{'));
    }
}
