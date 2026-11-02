<?php

declare(strict_types=1);

namespace Pulse\Tests\Unit;

use PHPUnit\Framework\Attributes\DataProvider;
use PHPUnit\Framework\TestCase;
use Pulse\Validation\MonitorInput;
use Pulse\Validation\ValidationException;

final class MonitorInputTest extends TestCase
{
    public function testCreateAppliesDefaults(): void
    {
        $values = MonitorInput::forCreate(['name' => '  target ', 'url' => 'http://target:8090/']);

        self::assertSame([
            'name' => 'target',
            'url' => 'http://target:8090/',
            'method' => 'GET',
            'interval_seconds' => 60,
            'timeout_ms' => 5000,
            'expected_status' => 200,
        ], $values);
    }

    public function testCreateAcceptsAllFields(): void
    {
        $values = MonitorInput::forCreate([
            'name' => 'api',
            'url' => 'https://example.com/health?full=1',
            'method' => 'head',
            'interval_seconds' => 10,
            'timeout_ms' => 30000,
            'expected_status' => 599,
            'unknown_field' => 'ignored',
        ]);

        self::assertSame('HEAD', $values['method']);
        self::assertSame(10, $values['interval_seconds']);
        self::assertSame(30000, $values['timeout_ms']);
        self::assertSame(599, $values['expected_status']);
        self::assertArrayNotHasKey('unknown_field', $values);
    }

    public function testCreateRequiresNameAndUrl(): void
    {
        $fields = $this->fieldsOf(static fn () => MonitorInput::forCreate([]));

        self::assertSame(['name' => 'is required', 'url' => 'is required'], $fields);
    }

    public function testAllProblemsAreReportedAtOnce(): void
    {
        $fields = $this->fieldsOf(static fn () => MonitorInput::forCreate([
            'name' => '',
            'url' => 'ftp://example.com',
            'interval_seconds' => 5,
            'timeout_ms' => 99,
            'expected_status' => 600,
            'method' => 'POST',
        ]));

        self::assertSame(['name', 'url', 'interval_seconds', 'timeout_ms', 'expected_status', 'method'], array_keys($fields));
    }

    /** @return iterable<string, array{mixed}> */
    public static function invalidUrls(): iterable
    {
        yield 'not a string' => [42];
        yield 'empty' => [''];
        yield 'relative' => ['/health'];
        yield 'no host' => ['http://'];
        yield 'ftp' => ['ftp://example.com/file'];
        yield 'javascript' => ['javascript:alert(1)'];
        yield 'with space' => ['http://exa mple.com/'];
        yield 'too long' => ['http://example.com/' . str_repeat('a', 2048)];
    }

    #[DataProvider('invalidUrls')]
    public function testInvalidUrl(mixed $url): void
    {
        $fields = $this->fieldsOf(static fn () => MonitorInput::forCreate(['name' => 'x', 'url' => $url]));

        self::assertArrayHasKey('url', $fields);
    }

    public function testUrlOfExactlyMaxLengthIsAccepted(): void
    {
        $url = 'http://example.com/';
        $url .= str_repeat('a', MonitorInput::MAX_URL_LENGTH - strlen($url));

        self::assertSame($url, MonitorInput::forCreate(['name' => 'x', 'url' => $url])['url']);
    }

    /** @return iterable<string, array{string, mixed, bool}> */
    public static function integerBounds(): iterable
    {
        yield 'interval min' => ['interval_seconds', 10, true];
        yield 'interval max' => ['interval_seconds', 3600, true];
        yield 'interval below' => ['interval_seconds', 9, false];
        yield 'interval above' => ['interval_seconds', 3601, false];
        yield 'interval as string' => ['interval_seconds', '60', false];
        yield 'interval as float' => ['interval_seconds', 60.0, false];
        yield 'timeout min' => ['timeout_ms', 100, true];
        yield 'timeout max' => ['timeout_ms', 30000, true];
        yield 'timeout above' => ['timeout_ms', 30001, false];
        yield 'status min' => ['expected_status', 100, true];
        yield 'status below' => ['expected_status', 99, false];
        yield 'status as bool' => ['expected_status', true, false];
        yield 'null' => ['timeout_ms', null, false];
    }

    #[DataProvider('integerBounds')]
    public function testIntegerBounds(string $field, mixed $value, bool $valid): void
    {
        $data = ['name' => 'x', 'url' => 'http://x/', $field => $value];

        if ($valid) {
            self::assertSame($value, MonitorInput::forCreate($data)[$field]);
        } else {
            self::assertArrayHasKey($field, $this->fieldsOf(static fn () => MonitorInput::forCreate($data)));
        }
    }

    public function testUpdateReturnsOnlySentFields(): void
    {
        self::assertSame(['paused' => true], MonitorInput::forUpdate(['paused' => true]));
        self::assertSame([], MonitorInput::forUpdate([]));
    }

    public function testUpdateValidatesSentFields(): void
    {
        $fields = $this->fieldsOf(static fn () => MonitorInput::forUpdate(['paused' => 'yes', 'url' => 'nope', 'name' => null]));

        self::assertSame(['name', 'url', 'paused'], array_keys($fields));
    }

    public function testCreateIgnoresPaused(): void
    {
        self::assertArrayNotHasKey('paused', MonitorInput::forCreate(['name' => 'x', 'url' => 'http://x/', 'paused' => true]));
    }

    /** @return array<string, string> */
    private function fieldsOf(\Closure $call): array
    {
        try {
            $call();
        } catch (ValidationException $e) {
            return $e->fields;
        }
        self::fail('ValidationException was not thrown');
    }
}
