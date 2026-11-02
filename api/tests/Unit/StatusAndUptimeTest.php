<?php

declare(strict_types=1);

namespace Pulse\Tests\Unit;

use PHPUnit\Framework\Attributes\DataProvider;
use PHPUnit\Framework\TestCase;
use Pulse\Repository\UptimeRepository;
use Pulse\Status\Overall;

final class StatusAndUptimeTest extends TestCase
{
    /** @return iterable<string, array{list<string>, string}> */
    public static function overallCases(): iterable
    {
        yield 'no monitors' => [[], 'unknown'];
        yield 'only unknown' => [['unknown', 'unknown'], 'unknown'];
        yield 'all up' => [['up', 'up'], 'up'];
        yield 'up and not yet checked' => [['up', 'unknown'], 'up'];
        yield 'all down' => [['down'], 'down'];
        yield 'down and unknown' => [['down', 'unknown'], 'down'];
        yield 'mixed' => [['up', 'down', 'unknown'], 'degraded'];
    }

    /** @param list<string> $statuses */
    #[DataProvider('overallCases')]
    public function testOverall(array $statuses, string $expected): void
    {
        self::assertSame($expected, Overall::compute($statuses));
    }

    public function testUptimeRowIsAggregatedForTheApi(): void
    {
        $item = UptimeRepository::toApi([
            'hour' => '2026-10-12 13:00:00+03',
            'checks' => 60,
            'ok' => 57,
            'latency_sum_ms' => 3001,
            'latency_max_ms' => 420,
        ]);

        self::assertSame([
            'hour' => '2026-10-12T10:00:00Z',
            'checks' => 60,
            'ok' => 57,
            'uptime' => 0.95,
            'latency_avg_ms' => 50.0,
            'latency_max_ms' => 420,
        ], $item);
    }

    public function testUptimeRoundsToFourDigits(): void
    {
        $item = UptimeRepository::toApi(['hour' => '2026-10-12 10:00:00+00', 'checks' => 3, 'ok' => 2, 'latency_sum_ms' => 100, 'latency_max_ms' => 50]);

        self::assertSame(0.6667, $item['uptime']);
        self::assertSame(33.3, $item['latency_avg_ms']);
    }

    public function testHourWithoutChecksGivesNullsInsteadOfDivisionByZero(): void
    {
        $item = UptimeRepository::toApi(['hour' => '2026-10-12 10:00:00+00', 'checks' => 0, 'ok' => 0, 'latency_sum_ms' => 0, 'latency_max_ms' => 0]);

        self::assertNull($item['uptime']);
        self::assertNull($item['latency_avg_ms']);
    }
}
