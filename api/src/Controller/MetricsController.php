<?php

declare(strict_types=1);

namespace Pulse\Controller;

use Closure;
use PDO;
use Predis\ClientInterface;
use Pulse\Http\Response;
use Pulse\Logger;
use Pulse\Metrics\Prometheus;
use Pulse\Metrics\RequestMetrics;
use Pulse\Repository\IncidentRepository;

/**
 * GET /metrics. Always answers 200: if Valkey or PostgreSQL is down, the affected families are
 * left out (and a warning is logged) so the scrape itself, and `up`, keep working.
 */
final readonly class MetricsController
{
    /** Values of the `status` label of pulse_monitors; paused monitors are counted separately. */
    private const array MONITOR_STATUSES = ['unknown', 'up', 'down', 'paused'];

    /**
     * @param Closure(): ClientInterface $valkey
     * @param Closure(): PDO             $db
     */
    public function __construct(
        private Closure $valkey,
        private Closure $db,
        private Logger $logger,
    ) {
    }

    public function show(): Response
    {
        $body = '';

        try {
            $body .= (new RequestMetrics(($this->valkey)()))->render();
        } catch (\Throwable $e) {
            $this->logger->warn('metrics: cannot read request counters from valkey', ['error' => $e->getMessage()]);
        }

        try {
            $body .= $this->databaseGauges(($this->db)());
        } catch (\Throwable $e) {
            $this->logger->warn('metrics: cannot read gauges from postgres', ['error' => $e->getMessage()]);
        }

        return new Response(200, $body, ['Content-Type' => Prometheus::CONTENT_TYPE]);
    }

    private function databaseGauges(PDO $db): string
    {
        $counts = array_fill_keys(self::MONITOR_STATUSES, 0);
        $rows = $db->query(
            "SELECT CASE WHEN paused THEN 'paused' ELSE status END AS status, count(*) AS n FROM monitors GROUP BY 1",
        )->fetchAll();
        foreach ($rows as $row) {
            $counts[(string) $row['status']] = (int) $row['n'];
        }

        $samples = [];
        foreach ($counts as $status => $n) {
            $samples[] = ['labels' => ['status' => (string) $status], 'value' => $n];
        }

        return Prometheus::gauge('pulse_monitors', 'Monitors by current status (paused monitors have status="paused").', $samples)
            . Prometheus::gauge('pulse_open_incidents', 'Incidents that are not resolved yet.', [
                ['labels' => [], 'value' => (new IncidentRepository($db))->countOpen()],
            ]);
    }
}
