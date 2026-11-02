<?php

declare(strict_types=1);

namespace Pulse\Controller;

use Pulse\Http\HttpException;
use Pulse\Http\Request;
use Pulse\Http\Response;
use Pulse\Repository\CheckRepository;
use Pulse\Repository\MonitorRepository;
use Pulse\Repository\UptimeRepository;
use Pulse\Validation\MonitorInput;

final readonly class MonitorController
{
    public function __construct(
        private MonitorRepository $monitors,
        private CheckRepository $checks,
    ) {
    }

    public function list(): Response
    {
        return Response::json(200, ['items' => $this->monitors->all()]);
    }

    public function create(Request $request): Response
    {
        $monitor = $this->monitors->create(MonitorInput::forCreate($request->jsonObject()));

        return Response::json(201, $monitor, ['Location' => '/api/v1/monitors/' . $monitor['id']]);
    }

    public function show(int $id): Response
    {
        return Response::json(200, $this->monitors->find($id) ?? throw HttpException::notFound());
    }

    public function update(int $id, Request $request): Response
    {
        $changes = MonitorInput::forUpdate($request->jsonObject());

        return Response::json(200, $this->monitors->update($id, $changes) ?? throw HttpException::notFound());
    }

    public function delete(int $id): Response
    {
        if (!$this->monitors->delete($id)) {
            throw HttpException::notFound();
        }

        return Response::noContent();
    }

    public function checks(int $id, Request $request): Response
    {
        $limit = $request->queryInt('limit', 50, 1, 500);
        if (!$this->monitors->exists($id)) {
            throw HttpException::notFound();
        }

        return Response::json(200, ['items' => $this->checks->forMonitor($id, $limit)]);
    }

    /** GET /api/v1/monitors/{id}/uptime?days=30: hourly aggregates from the Kafka pipeline (empty without it). */
    public function uptime(int $id, Request $request, UptimeRepository $uptime): Response
    {
        $days = $request->queryInt('days', 30, 1, UptimeRepository::MAX_DAYS);
        if (!$this->monitors->exists($id)) {
            throw HttpException::notFound();
        }

        return Response::json(200, ['items' => $uptime->hourly($id, $days)]);
    }
}
