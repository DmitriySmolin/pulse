<?php

declare(strict_types=1);

// Front controller: PHP-FPM SCRIPT_FILENAME must point here for every API path
// (/api/*, /healthz, /readyz, /metrics, /debug/*).

require dirname(__DIR__) . '/vendor/autoload.php';

Pulse\Http\Kernel::fromEnvironment()->run();
