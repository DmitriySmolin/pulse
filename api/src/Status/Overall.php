<?php

declare(strict_types=1);

namespace Pulse\Status;

final class Overall
{
    /**
     * Overall status for the status page. Monitors without data yet ("unknown") are ignored:
     *   no known statuses -> unknown; all up -> up; all down -> down; both -> degraded.
     *
     * @param list<string> $statuses statuses of active (not paused) monitors
     */
    public static function compute(array $statuses): string
    {
        $up = in_array('up', $statuses, true);
        $down = in_array('down', $statuses, true);

        return match (true) {
            $up && $down => 'degraded',
            $up => 'up',
            $down => 'down',
            default => 'unknown',
        };
    }
}
