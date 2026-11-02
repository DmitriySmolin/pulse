<?php

declare(strict_types=1);

namespace Pulse\Controller;

use Pulse\Http\Request;
use Pulse\Http\Response;
use Pulse\Repository\IncidentRepository;
use Pulse\Validation\ValidationException;

final readonly class IncidentController
{
    public function __construct(private IncidentRepository $incidents)
    {
    }

    /** GET /api/v1/incidents?open=1|0&limit=100 (no `open` = all incidents). */
    public function list(Request $request): Response
    {
        $open = match ($request->queryString('open')) {
            null, '' => null,
            '1', 'true' => true,
            '0', 'false' => false,
            default => throw new ValidationException(['open' => 'must be 1 or 0']),
        };
        $limit = $request->queryInt('limit', 100, 1, 500);

        return Response::json(200, ['items' => $this->incidents->list($open, $limit)]);
    }
}
