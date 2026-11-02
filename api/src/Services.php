<?php

declare(strict_types=1);

namespace Pulse;

use PDO;
use Predis\ClientInterface;
use Pulse\Infra\Database;
use Pulse\Infra\Valkey;

/**
 * Lazily created connections for one request (or one console command).
 * Nothing connects until it is used: /healthz never opens a DB connection.
 */
final class Services
{
    private ?PDO $db = null;
    private ?ClientInterface $valkey = null;

    public function __construct(
        public readonly Config $config,
        public readonly Logger $logger,
    ) {
    }

    public function db(): PDO
    {
        return $this->db ??= Database::connect($this->config->databaseUrl());
    }

    public function valkey(): ClientInterface
    {
        return $this->valkey ??= Valkey::connect($this->config->valkeyUrl());
    }
}
