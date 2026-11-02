<?php

declare(strict_types=1);

namespace Pulse\Tests\Integration;

use Pulse\Config;
use Pulse\Http\Kernel;
use Pulse\Http\Outcome;
use Pulse\Http\Request;
use Pulse\Metrics\RequestMetrics;
use Pulse\Status\StatusService;

/** The whole HTTP API through Kernel::handle() against real PostgreSQL and Valkey. */
final class ApiTest extends IntegrationTestCase
{
    private const string TOKEN = 'test-token';

    private Kernel $kernel;

    protected function setUp(): void
    {
        parent::setUp();
        $valkeyUrl = $this->valkeyUrlOrSkip();
        $this->migrate();
        $this->valkey()->del([StatusService::CACHE_KEY, RequestMetrics::REQUESTS_KEY, RequestMetrics::DURATION_KEY]);

        $this->kernel = new Kernel(Config::fromArray([
            'DATABASE_URL' => $this->databaseUrl(),
            'VALKEY_URL' => $valkeyUrl,
            'PULSE_API_TOKEN' => self::TOKEN,
            'PULSE_STATUS_CACHE_TTL' => '10',
        ]), $this->logger());
    }

    /**
     * @param array<string, mixed>|null $body
     *
     * @return array{0: int, 1: mixed, 2: Outcome}
     */
    private function call(string $method, string $uri, ?array $body = null, bool $auth = true): array
    {
        $headers = $auth ? ['Authorization' => 'Bearer ' . self::TOKEN] : [];
        $request = Request::create($method, $uri, $headers, $body === null ? '' : json_encode($body, JSON_THROW_ON_ERROR));
        $outcome = $this->kernel->handle($request);
        $this->kernel->terminate($request, $outcome);
        $decoded = $outcome->response->body === '' ? null : json_decode($outcome->response->body, true);

        return [$outcome->response->status, $decoded, $outcome];
    }

    public function testReadiness(): void
    {
        [$status, $body] = $this->call('GET', '/readyz');

        self::assertSame(200, $status);
        self::assertSame(['status' => 'ready', 'checks' => ['postgres' => 'ok', 'valkey' => 'ok']], $body);
    }

    public function testMonitorCrud(): void
    {
        [$status, $created, $outcome] = $this->call('POST', '/api/v1/monitors', ['name' => 'target', 'url' => 'http://target:8090/']);
        self::assertSame(201, $status);
        self::assertSame('/api/v1/monitors/' . $created['id'], $outcome->response->header('Location'));
        self::assertSame(
            ['id', 'name', 'url', 'method', 'interval_seconds', 'timeout_ms', 'expected_status', 'status', 'paused',
                'consecutive_failures', 'last_checked_at', 'next_check_at', 'created_at'],
            array_keys($created),
        );
        self::assertSame(['GET', 60, 5000, 200, 'unknown', false], [
            $created['method'], $created['interval_seconds'], $created['timeout_ms'], $created['expected_status'], $created['status'], $created['paused'],
        ]);
        self::assertMatchesRegularExpression('/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$/', $created['created_at']);
        $id = $created['id'];

        [$status, $list] = $this->call('GET', '/api/v1/monitors');
        self::assertSame(200, $status);
        self::assertSame([$id], array_column($list['items'], 'id'));

        [$status, $patched] = $this->call('PATCH', "/api/v1/monitors/{$id}", ['paused' => true, 'interval_seconds' => 30]);
        self::assertSame(200, $status);
        self::assertTrue($patched['paused']);
        self::assertSame(30, $patched['interval_seconds']);

        self::assertSame(200, $this->call('GET', "/api/v1/monitors/{$id}")[0]);
        self::assertSame(204, $this->call('DELETE', "/api/v1/monitors/{$id}")[0]);
        self::assertSame(404, $this->call('GET', "/api/v1/monitors/{$id}")[0]);
        self::assertSame(404, $this->call('DELETE', "/api/v1/monitors/{$id}")[0]);
        self::assertSame(404, $this->call('PATCH', "/api/v1/monitors/{$id}", ['paused' => false])[0]);
    }

    public function testValidationAndAuth(): void
    {
        [$status, $body] = $this->call('POST', '/api/v1/monitors', ['name' => '', 'url' => 'ftp://x/', 'interval_seconds' => 5, 'method' => 'POST']);
        self::assertSame(422, $status);
        self::assertSame('validation', $body['error']);
        self::assertSame(['name', 'url', 'interval_seconds', 'method'], array_keys($body['fields']));

        self::assertSame(401, $this->call('POST', '/api/v1/monitors', ['name' => 'x', 'url' => 'http://x/'], auth: false)[0]);
        self::assertSame(401, $this->call('DELETE', '/api/v1/monitors/1', auth: false)[0]);
        self::assertSame(400, $this->call('POST', '/api/v1/monitors')[0], 'empty body is not JSON');
        self::assertSame(422, $this->call('GET', '/api/v1/monitors/1/checks?limit=501')[0]);
    }

    public function testChecksIncidentsAndUptime(): void
    {
        $id = (int) $this->db->query("INSERT INTO monitors (name, url, status) VALUES ('m', 'http://t/', 'down') RETURNING id")->fetchColumn();
        $this->db->exec("INSERT INTO checks (monitor_id, checked_at, ok, status_code, latency_ms)
                         SELECT {$id}, now() - make_interval(secs => g), g % 2 = 0, 200, g FROM generate_series(1, 10) g");
        $this->db->exec("INSERT INTO incidents (monitor_id, started_at, resolved_at, cause) VALUES
                         ({$id}, now() - interval '2 hours', now() - interval '1 hour', 'old'), ({$id}, now(), NULL, 'unexpected status 503')");
        $this->db->exec("INSERT INTO uptime_hourly (monitor_id, hour, checks, ok, latency_sum_ms, latency_max_ms) VALUES
                         ({$id}, date_trunc('hour', now()) - interval '30 days', 60, 60, 600, 10),
                         ({$id}, date_trunc('hour', now()) - interval '2 hours', 60, 30, 6000, 900),
                         ({$id}, date_trunc('hour', now()) - interval '1 hour', 4, 4, 40, 12)");

        [$status, $checks] = $this->call('GET', "/api/v1/monitors/{$id}/checks?limit=3");
        self::assertSame(200, $status);
        self::assertCount(3, $checks['items']);
        self::assertSame([1, 2, 3], array_column($checks['items'], 'latency_ms'), 'newest first');

        [, $open] = $this->call('GET', '/api/v1/incidents?open=1');
        self::assertSame(['unexpected status 503'], array_column($open['items'], 'cause'));
        [, $all] = $this->call('GET', '/api/v1/incidents');
        self::assertCount(2, $all['items']);

        [$status, $uptime] = $this->call('GET', "/api/v1/monitors/{$id}/uptime?days=7");
        self::assertSame(200, $status);
        self::assertCount(2, $uptime['items'], 'rows older than `days` are not returned');
        self::assertSame(['hour', 'checks', 'ok', 'uptime', 'latency_avg_ms', 'latency_max_ms'], array_keys($uptime['items'][0]));
        self::assertSame([0.5, 1.0], array_column($uptime['items'], 'uptime'), 'oldest first');
        self::assertSame([100.0, 10.0], array_column($uptime['items'], 'latency_avg_ms'));

        self::assertCount(3, $this->call('GET', "/api/v1/monitors/{$id}/uptime?days=90")[1]['items']);
        self::assertSame(422, $this->call('GET', "/api/v1/monitors/{$id}/uptime?days=0")[0]);
        self::assertSame(
            [422, ['error' => 'validation', 'fields' => ['days' => 'must be an integer between 1 and 90']]],
            array_slice($this->call('GET', "/api/v1/monitors/{$id}/uptime?days=91"), 0, 2),
        );
        self::assertSame(404, $this->call('GET', '/api/v1/monitors/999999/uptime')[0]);
        self::assertSame(404, $this->call('GET', '/api/v1/monitors/999999/checks')[0]);
    }

    public function testUptimeIsEmptyWithoutKafkaPipeline(): void
    {
        [, $created] = $this->call('POST', '/api/v1/monitors', ['name' => 'x', 'url' => 'https://example.com/']);

        self::assertSame(['items' => []], $this->call('GET', "/api/v1/monitors/{$created['id']}/uptime")[1]);
    }

    public function testStatusSummaryIsCachedInValkey(): void
    {
        $up = (int) $this->db->query("INSERT INTO monitors (name, url, status, last_checked_at) VALUES ('a-up', 'http://a/', 'up', now()) RETURNING id")->fetchColumn();
        $this->db->exec("INSERT INTO monitors (name, url, status) VALUES ('b-down', 'http://b/', 'down')");
        $this->db->exec("INSERT INTO monitors (name, url, status, paused) VALUES ('c-paused', 'http://c/', 'down', true)");
        $this->db->exec("INSERT INTO checks (monitor_id, ok, checked_at) VALUES ({$up}, true, now()), ({$up}, true, now()), ({$up}, false, now()),
                         ({$up}, false, now() - interval '25 hours')");

        [$status, $body, $outcome] = $this->call('GET', '/api/v1/status');
        self::assertSame(200, $status);
        self::assertSame('MISS', $outcome->response->header('X-Cache'));
        self::assertSame(['generated_at', 'overall', 'monitors', 'open_incidents'], array_keys($body));
        self::assertSame('degraded', $body['overall'], 'paused monitors do not count');
        self::assertSame(['a-up', 'b-down'], array_column($body['monitors'], 'name'));
        self::assertSame(['id', 'name', 'status', 'uptime_24h', 'last_checked_at'], array_keys($body['monitors'][0]));
        self::assertSame(0.6667, $body['monitors'][0]['uptime_24h']);
        self::assertNull($body['monitors'][1]['uptime_24h'], 'no checks in 24h -> null, not 0');

        // a change in the DB is not visible until the cache expires: that is the point of the cache
        $this->db->exec("UPDATE monitors SET status = 'up'");
        [, $cached, $outcome] = $this->call('GET', '/api/v1/status');
        self::assertSame('HIT', $outcome->response->header('X-Cache'));
        self::assertSame($body, $cached);
        $ttl = (int) $this->valkey()->ttl(StatusService::CACHE_KEY);
        self::assertGreaterThan(0, $ttl);
        self::assertLessThanOrEqual(10, $ttl);

        $this->valkey()->del([StatusService::CACHE_KEY]);
        self::assertSame('up', $this->call('GET', '/api/v1/status')[1]['overall']);
    }

    public function testMetricsAreSharedThroughValkey(): void
    {
        $this->call('GET', '/api/v1/monitors');
        $this->call('GET', '/api/v1/monitors');
        $this->call('GET', '/api/v1/monitors/12345');
        $this->call('GET', '/no/such/path');
        $this->db->exec("INSERT INTO monitors (name, url, status) VALUES ('a', 'http://a/', 'up'), ('b', 'http://b/', 'down')");
        $this->db->exec("INSERT INTO incidents (monitor_id) SELECT id FROM monitors WHERE status = 'down'");

        [$status, , $outcome] = $this->call('GET', '/metrics');
        $text = $outcome->response->body;

        self::assertSame(200, $status);
        self::assertStringStartsWith('text/plain; version=0.0.4', (string) $outcome->response->header('Content-Type'));
        self::assertStringContainsString('pulse_api_http_requests_total{route="/api/v1/monitors",method="GET",code="200"} 2', $text);
        self::assertStringContainsString('pulse_api_http_requests_total{route="/api/v1/monitors/{id}",method="GET",code="404"} 1', $text);
        self::assertStringContainsString('pulse_api_http_requests_total{route="unmatched",method="GET",code="404"} 1', $text);
        self::assertStringContainsString('pulse_api_http_request_duration_seconds_bucket{route="/api/v1/monitors",le="+Inf"} 2', $text);
        self::assertStringContainsString('pulse_api_http_request_duration_seconds_count{route="/api/v1/monitors"} 2', $text);
        self::assertStringContainsString('pulse_monitors{status="up"} 1', $text);
        self::assertStringContainsString('pulse_monitors{status="down"} 1', $text);
        self::assertStringContainsString('pulse_monitors{status="unknown"} 0', $text);
        self::assertStringContainsString('pulse_open_incidents 1', $text);
        self::assertStringNotContainsString('12345', $text, 'ids never become label values');
    }
}
