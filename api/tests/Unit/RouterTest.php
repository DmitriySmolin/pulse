<?php

declare(strict_types=1);

namespace Pulse\Tests\Unit;

use PHPUnit\Framework\TestCase;
use Pulse\Http\Request;
use Pulse\Http\Response;
use Pulse\Http\RouteMatch;
use Pulse\Http\Router;

final class RouterTest extends TestCase
{
    private Router $router;

    protected function setUp(): void
    {
        $ok = static fn (): Response => Response::json(200, []);
        $this->router = new Router();
        $this->router->add('GET', '/healthz', $ok);
        $this->router->add('GET', '/api/v1/monitors', $ok);
        $this->router->add('POST', '/api/v1/monitors', $ok);
        $this->router->add('GET', '/api/v1/monitors/{id}', $ok);
        $this->router->add('PATCH', '/api/v1/monitors/{id}', $ok);
        $this->router->add('GET', '/api/v1/monitors/{id}/uptime', $ok);
    }

    public function testStaticRoute(): void
    {
        $match = $this->router->match('GET', '/healthz');

        self::assertSame('/healthz', $match->route);
        self::assertNotNull($match->handler);
        self::assertSame([], $match->params);
    }

    public function testRouteLabelIsTheTemplateAndParamsAreInts(): void
    {
        $match = $this->router->match('GET', '/api/v1/monitors/42/uptime');

        self::assertSame('/api/v1/monitors/{id}/uptime', $match->route);
        self::assertSame(['id' => 42], $match->params);
    }

    public function testMethodSelectsTheHandler(): void
    {
        self::assertNotNull($this->router->match('POST', '/api/v1/monitors')->handler);
        self::assertNotNull($this->router->match('PATCH', '/api/v1/monitors/7')->handler);
    }

    public function testHeadIsServedByGet(): void
    {
        $match = $this->router->match('HEAD', '/api/v1/monitors/7');

        self::assertNotNull($match->handler);
        self::assertSame('/api/v1/monitors/{id}', $match->route);
    }

    public function testWrongMethodIs405WithAllowList(): void
    {
        $match = $this->router->match('DELETE', '/api/v1/monitors');

        self::assertNull($match->handler);
        self::assertSame('/api/v1/monitors', $match->route);
        self::assertSame(['GET', 'POST', 'HEAD'], $match->allowedMethods);
    }

    /** Anything that is not a known template must collapse into one label: no cardinality explosion. */
    public function testUnknownPathsAreUnmatched(): void
    {
        foreach (['/', '/api/v1/monitors/abc', '/api/v1/monitors/1/2', '/api/v1/monitors/', '/wp-login.php', '/api/v1/monitors/1234567890123456789'] as $path) {
            $match = $this->router->match('GET', $path);
            self::assertSame(RouteMatch::UNMATCHED, $match->route, $path);
            self::assertNull($match->handler, $path);
            self::assertSame([], $match->allowedMethods, $path);
        }
    }

    public function testRequestCreateParsesPathAndQuery(): void
    {
        $request = Request::create('get', '/api/v1/monitors/1/checks?limit=5', ['X-Request-Id' => 'abc-123']);

        self::assertSame('GET', $request->method);
        self::assertSame('/api/v1/monitors/1/checks', $request->path);
        self::assertSame(5, $request->queryInt('limit', 50, 1, 500));
        self::assertSame('abc-123', $request->requestId);
    }

    public function testInsaneRequestIdIsReplacedWithUuid(): void
    {
        $request = Request::create('GET', '/', ['X-Request-Id' => "evil\nlog-injection"]);

        self::assertMatchesRegularExpression('/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/', $request->requestId);
    }

    public function testBearerToken(): void
    {
        self::assertSame('t0k', Request::create('POST', '/', ['Authorization' => 'Bearer t0k'])->bearerToken());
        self::assertNull(Request::create('POST', '/', ['Authorization' => 'Basic dTpw'])->bearerToken());
        self::assertNull(Request::create('POST', '/')->bearerToken());
    }
}
