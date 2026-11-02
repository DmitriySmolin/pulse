package checker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"pulse/internal/message"
)

const (
	maxRedirects = 5
	bodyLimit    = 64 << 10 // what goes into the snapshot
)

// Result of one HTTP check.
type Result struct {
	CheckedAt  time.Time
	OK         bool
	StatusCode int // 0 when there was no response
	Latency    time.Duration
	Err        string // why the check failed; empty when OK

	Proto  string
	Status string // "503 Service Unavailable"
	Header http.Header
	Body   []byte // first 64 KiB
}

// Prober performs HTTP checks.
type Prober struct {
	client    *http.Client
	userAgent string
}

// NewProber disables keep-alive: every check opens a new connection, so
// DNS, TCP and TLS problems are not hidden by a pooled connection.
func NewProber(userAgent string) *Prober {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableKeepAlives = true
	return &Prober{
		client: &http.Client{
			Transport: transport,
			CheckRedirect: func(_ *http.Request, via []*http.Request) error {
				if len(via) > maxRedirects {
					return fmt.Errorf("stopped after %d redirects", maxRedirects)
				}
				return nil
			},
		},
		userAgent: userAgent,
	}
}

// Probe never returns an error: every failure is a failed check.
func (p *Prober) Probe(ctx context.Context, req message.CheckRequested) Result {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(req.TimeoutMS)*time.Millisecond)
	defer cancel()
	ctx, span := tracer.Start(ctx, "http probe", trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("http.request.method", req.Method), attribute.String("url.full", req.URL)))
	defer span.End()

	res := Result{CheckedAt: time.Now().UTC()}
	start := time.Now()
	finish := func(err error) Result {
		res.Latency = time.Since(start)
		if err != nil {
			res.Err = describe(ctx, err, req.TimeoutMS)
		} else if res.StatusCode != req.ExpectedStatus {
			res.Err = fmt.Sprintf("unexpected status %d, expected %d", res.StatusCode, req.ExpectedStatus)
		}
		res.OK = res.Err == ""
		span.SetAttributes(attribute.Int("http.response.status_code", res.StatusCode), attribute.Bool("pulse.ok", res.OK))
		return res
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.URL, nil)
	if err != nil {
		return finish(fmt.Errorf("invalid request: %w", err))
	}
	httpReq.Header.Set("User-Agent", p.userAgent)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(httpReq.Header))

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return finish(err)
	}
	defer resp.Body.Close()
	res.StatusCode, res.Proto, res.Status, res.Header = resp.StatusCode, resp.Proto, resp.Status, resp.Header

	res.Body, err = io.ReadAll(io.LimitReader(resp.Body, bodyLimit))
	if err != nil {
		return finish(fmt.Errorf("read body: %w", err))
	}
	return finish(nil)
}

// describe turns transport errors into short messages for checks.error.
func describe(ctx context.Context, err error, timeoutMS int) string {
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil || (errors.As(err, &netErr) && netErr.Timeout()) {
		return fmt.Sprintf("timeout after %dms", timeoutMS)
	}
	// *url.Error repeats the method and URL: `Get "http://...": dial tcp ...`.
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err.Error()
	}
	return err.Error()
}
