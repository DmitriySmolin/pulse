<?php

declare(strict_types=1);

namespace Pulse\Controller;

use PDO;
use Pulse\Http\Request;
use Pulse\Http\Response;
use Pulse\Validation\ValidationException;

/** Fault-injection endpoints. Routed only when PULSE_FAULTS_ENABLED=1, otherwise they are a plain 404. */
final readonly class DebugController
{
    public const int MAX_SLOW_QUERY_SECONDS = 60;

    public function __construct(private PDO $db)
    {
    }

    /** GET /debug/slow-query?seconds=N: holds a DB connection and an FPM worker for N seconds. */
    public function slowQuery(Request $request): Response
    {
        $raw = $request->queryString('seconds') ?? '1';
        $seconds = filter_var($raw, FILTER_VALIDATE_FLOAT);
        if ($seconds === false || $seconds < 0 || $seconds > self::MAX_SLOW_QUERY_SECONDS) {
            throw new ValidationException(['seconds' => sprintf('must be a number between 0 and %d', self::MAX_SLOW_QUERY_SECONDS)]);
        }

        $stmt = $this->db->prepare('SELECT pg_sleep(CAST(:seconds AS double precision))');
        $stmt->bindValue(':seconds', (string) $seconds);
        $stmt->execute();

        return Response::json(200, ['slept_seconds' => $seconds]);
    }
}
