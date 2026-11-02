<?php

declare(strict_types=1);

namespace Pulse\Controller;

use Pulse\Http\Response;
use Pulse\Status\StatusService;

final readonly class StatusController
{
    public function __construct(private StatusService $status)
    {
    }

    public function show(): Response
    {
        [$json, $hit] = $this->status->json();

        return Response::rawJson(200, $json, ['X-Cache' => $hit ? 'HIT' : 'MISS']);
    }
}
