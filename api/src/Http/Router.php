<?php

declare(strict_types=1);

namespace Pulse\Http;

use Closure;

/**
 * Minimal router. Templates look like /api/v1/monitors/{id}; every placeholder is a numeric id.
 * The template itself is used as the `route` label in logs and metrics (bounded cardinality).
 */
final class Router
{
    /** @var list<array{method: string, template: string, regex: string, handler: Closure}> */
    private array $routes = [];

    /** @param Closure(Request, array<string, int>): Response $handler */
    public function add(string $method, string $template, Closure $handler): void
    {
        $segments = array_map(
            static fn (string $s): string => preg_match('/^\{([a-z_]+)\}$/', $s, $m) === 1
                ? sprintf('(?P<%s>[0-9]{1,18})', $m[1])
                : preg_quote($s, '#'),
            explode('/', $template),
        );

        $this->routes[] = [
            'method' => strtoupper($method),
            'template' => $template,
            'regex' => '#^' . implode('/', $segments) . '$#',
            'handler' => $handler,
        ];
    }

    public function match(string $method, string $path): RouteMatch
    {
        $allowed = [];
        foreach ($this->routes as $route) {
            if (preg_match($route['regex'], $path, $m) !== 1) {
                continue;
            }
            // HEAD is served by the GET handler; the kernel drops the body.
            if ($route['method'] === $method || ($method === 'HEAD' && $route['method'] === 'GET')) {
                $params = [];
                foreach ($m as $key => $value) {
                    if (is_string($key)) {
                        $params[$key] = (int) $value;
                    }
                }

                return RouteMatch::found($route['template'], $route['handler'], $params);
            }
            $allowed[$route['template']][] = $route['method'];
        }

        if ($allowed !== []) {
            $template = array_key_first($allowed);
            $methods = $allowed[$template];
            if (in_array('GET', $methods, true)) {
                $methods[] = 'HEAD';
            }

            return RouteMatch::methodNotAllowed($template, $methods);
        }

        return RouteMatch::notFound();
    }
}
