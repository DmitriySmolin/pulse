<?php

declare(strict_types=1);

namespace Pulse\Tests\Unit;

use PHPUnit\Framework\Attributes\DataProvider;
use PHPUnit\Framework\TestCase;
use Pulse\Config;
use Pulse\ConfigException;

final class ConfigTest extends TestCase
{
    /** @var list<string> */
    private array $tempFiles = [];

    protected function tearDown(): void
    {
        foreach ($this->tempFiles as $file) {
            @unlink($file);
        }
    }

    public function testDefaults(): void
    {
        $config = Config::fromArray([]);

        self::assertSame(10, $config->statusCacheTtl);
        self::assertSame('info', $config->logLevel);
        self::assertFalse($config->faultsEnabled);
        self::assertSame(0, $config->faultLatencyMs);
        self::assertSame(0.0, $config->faultErrorRate);
        self::assertSame('http://target:8090', $config->seedTargetUrl);
    }

    public function testReadsPlainVariables(): void
    {
        $config = Config::fromArray([
            'DATABASE_URL' => 'postgres://pulse:pass@postgres:5432/pulse',
            'VALKEY_URL' => 'redis://valkey:6379/0',
            'PULSE_API_TOKEN' => 'secret',
            'PULSE_STATUS_CACHE_TTL' => '0',
            'LOG_LEVEL' => 'DEBUG',
            'PULSE_FAULTS_ENABLED' => '1',
            'PULSE_FAULT_LATENCY_MS' => '250',
            'PULSE_FAULT_ERROR_RATE' => '0.25',
        ]);

        self::assertSame('postgres://pulse:pass@postgres:5432/pulse', $config->databaseUrl());
        self::assertSame('redis://valkey:6379/0', $config->valkeyUrl());
        self::assertSame('secret', $config->apiToken());
        self::assertSame(0, $config->statusCacheTtl);
        self::assertSame('debug', $config->logLevel);
        self::assertTrue($config->faultsEnabled);
        self::assertSame(250, $config->faultLatencyMs);
        self::assertSame(0.25, $config->faultErrorRate);
    }

    public function testReadsValueFromFileAndStripsTrailingNewline(): void
    {
        $file = $this->tempFile("s3cr3t\n");

        $config = Config::fromArray(['PULSE_API_TOKEN_FILE' => $file]);

        self::assertSame('s3cr3t', $config->apiToken());
    }

    public function testAnyVariableMayComeFromFile(): void
    {
        $config = Config::fromArray(['PULSE_STATUS_CACHE_TTL_FILE' => $this->tempFile("42\r\n")]);

        self::assertSame(42, $config->statusCacheTtl);
    }

    public function testBothPlainAndFileFormsIsAnError(): void
    {
        $this->expectException(ConfigException::class);
        $this->expectExceptionMessage('both DATABASE_URL and DATABASE_URL_FILE are set');

        Config::fromArray([
            'DATABASE_URL' => 'postgres://a@b/c',
            'DATABASE_URL_FILE' => $this->tempFile('postgres://a@b/c'),
        ]);
    }

    public function testMissingFileIsAnError(): void
    {
        $this->expectException(ConfigException::class);
        $this->expectExceptionMessage('DATABASE_URL_FILE points to a missing or unreadable file');

        Config::fromArray(['DATABASE_URL_FILE' => '/nonexistent/pulse/db_url']);
    }

    public function testEmptyFileIsAnError(): void
    {
        $this->expectException(ConfigException::class);
        $this->expectExceptionMessage('empty file');

        Config::fromArray(['PULSE_API_TOKEN_FILE' => $this->tempFile("\n")]);
    }

    public function testEmptyValueCountsAsUnset(): void
    {
        $config = Config::fromArray(['DATABASE_URL' => '', 'DATABASE_URL_FILE' => $this->tempFile('postgres://x@y/z')]);

        self::assertSame('postgres://x@y/z', $config->databaseUrl());
    }

    public function testRequiredVariablesAreCheckedForApi(): void
    {
        $config = Config::fromArray(['DATABASE_URL' => 'postgres://a@b/c', 'VALKEY_URL' => 'redis://v:6379/0']);

        $this->expectException(ConfigException::class);
        $this->expectExceptionMessage('PULSE_API_TOKEN is required');

        $config->assertApiRequirements();
    }

    /** @return iterable<string, array{array<string, string>}> */
    public static function invalidValues(): iterable
    {
        yield 'ttl not a number' => [['PULSE_STATUS_CACHE_TTL' => 'ten']];
        yield 'ttl negative' => [['PULSE_STATUS_CACHE_TTL' => '-1']];
        yield 'log level' => [['LOG_LEVEL' => 'verbose']];
        yield 'faults flag' => [['PULSE_FAULTS_ENABLED' => 'maybe']];
        yield 'error rate above 1' => [['PULSE_FAULT_ERROR_RATE' => '1.5']];
        yield 'latency not int' => [['PULSE_FAULT_LATENCY_MS' => '1.5']];
    }

    /** @param array<string, string> $env */
    #[DataProvider('invalidValues')]
    public function testInvalidValuesAreRejected(array $env): void
    {
        $this->expectException(ConfigException::class);

        Config::fromArray($env);
    }

    private function tempFile(string $content): string
    {
        $file = tempnam(sys_get_temp_dir(), 'pulse-config-');
        self::assertIsString($file);
        file_put_contents($file, $content);
        $this->tempFiles[] = $file;

        return $file;
    }
}
