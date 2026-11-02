package amqpx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// ErrReject marks a message that can never be processed (e.g. invalid JSON).
// It goes straight to pulse.dead, without retries.
// Wrap it: fmt.Errorf("%w: %v", amqpx.ErrReject, err).
var ErrReject = errors.New("message rejected")

// failedDeliveries counts what happened to messages whose processing failed.
var failedDeliveries = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "pulse_amqp_failed_deliveries_total",
	Help: "Failed message processing by action: requeue (nack), retry (delayed retry queue), park (pulse.dead).",
}, []string{"queue", "action"})

// Handler processes one delivery. nil → ack; ErrReject → dead letter;
// any other error → retry according to Consumer.Retry.
// ctx is not cancelled by SIGTERM: the started work is finished.
type Handler func(ctx context.Context, d amqp.Delivery) error

// Consumer reads a queue with a pool of workers and reconnects when the
// channel or connection is lost.
type Consumer struct {
	Conn     *Conn
	Queue    string
	Prefetch int
	Workers  int
	Handler  Handler
	Log      *slog.Logger
	// RetryDelay is a pause before nack+requeue (requeue mode), so that a
	// short outage of the database does not burn all attempts in milliseconds.
	RetryDelay time.Duration
	// Retry selects delayed retries through pulse.checks.retry; the zero
	// value keeps the requeue mode.
	Retry Retry
}

var tracer = otel.Tracer("pulse/amqpx")

// Run blocks until ctx is cancelled and all in-progress messages are handled.
// Prefetched but not started messages return to the queue when the channel closes.
func (c *Consumer) Run(ctx context.Context) {
	for ctx.Err() == nil {
		err := c.consume(ctx)
		if ctx.Err() != nil {
			return
		}
		c.Log.Warn("consumer stopped, restarting", "queue", c.Queue, "error", err.Error())
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}

func (c *Consumer) consume(ctx context.Context) error {
	ch, err := c.Conn.Channel(ctx)
	if err != nil {
		return err
	}
	defer ch.Close()

	if err := ch.Qos(c.Prefetch, 0, false); err != nil {
		return fmt.Errorf("set prefetch: %w", err)
	}
	if c.Retry.Delayed {
		// Parking publishes to pulse.dlx on this channel and waits for the
		// broker's confirm before acking the original.
		if err := ch.Confirm(false); err != nil {
			return fmt.Errorf("enable publisher confirms: %w", err)
		}
	}
	deliveries, err := ch.Consume(c.Queue, "", false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("consume %s: %w", c.Queue, err)
	}
	c.Log.Info("consuming", "queue", c.Queue, "workers", c.Workers, "prefetch", c.Prefetch,
		"retry_mode", c.retryMode())

	var wg sync.WaitGroup
	for range c.Workers {
		wg.Go(func() {
			for {
				select {
				case <-ctx.Done():
					return
				case d, ok := <-deliveries:
					if !ok || ctx.Err() != nil {
						return // closed, or shutdown started: leave d unacked, it will be requeued
					}
					c.handle(ch, d)
				}
			}
		})
	}
	wg.Wait()
	if ctx.Err() != nil {
		return nil
	}
	return errors.New("delivery channel closed (channel or connection lost)")
}

func (c *Consumer) retryMode() string {
	if c.Retry.Delayed {
		return "delayed"
	}
	return "requeue"
}

func (c *Consumer) handle(ch *amqp.Channel, d amqp.Delivery) {
	ctx := otel.GetTextMapPropagator().Extract(context.Background(), HeaderCarrier(d.Headers))
	ctx, span := tracer.Start(ctx, c.Queue+" process", trace.WithSpanKind(trace.SpanKindConsumer))
	defer span.End()

	err := c.Handler(ctx, d)
	if err == nil {
		if err := d.Ack(false); err != nil {
			c.Log.WarnContext(ctx, "ack failed, message will be redelivered", "queue", c.Queue, "error", err.Error())
		}
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())

	if c.Retry.Delayed {
		c.failDelayed(ctx, ch, d, err)
		return
	}
	if errors.Is(err, ErrReject) {
		c.Log.WarnContext(ctx, "message rejected, dead-lettering", "queue", c.Queue, "error", err.Error())
		failedDeliveries.WithLabelValues(c.Queue, "park").Inc()
		_ = d.Reject(false)
		return
	}
	c.Log.ErrorContext(ctx, "message processing failed, requeueing", "queue", c.Queue,
		"error", err.Error(), "redelivered", d.Redelivered, "delivery_count", d.Headers["x-delivery-count"])
	failedDeliveries.WithLabelValues(c.Queue, "requeue").Inc()
	time.Sleep(c.RetryDelay)
	_ = d.Nack(false, true)
}

// failDelayed: reject into the retry queue, or park after Retry.Max attempts.
func (c *Consumer) failDelayed(ctx context.Context, ch *amqp.Channel, d amqp.Delivery, procErr error) {
	attempt := deathCount(d.Headers, c.Queue, "rejected") + 1
	if !errors.Is(procErr, ErrReject) && attempt < int64(c.Retry.Max) {
		c.Log.WarnContext(ctx, "message processing failed, delayed retry", "queue", c.Queue,
			"attempt", attempt, "max_attempts", c.Retry.Max, "retry_in", c.Retry.Delay.String(), "error", procErr.Error())
		failedDeliveries.WithLabelValues(c.Queue, "retry").Inc()
		_ = d.Reject(false) // dead-lettered to pulse.retry by the queue's x-dead-letter-exchange
		return
	}

	reason := "max attempts reached"
	if errors.Is(procErr, ErrReject) {
		reason = "unprocessable"
	}
	if err := c.park(ctx, ch, d, reason, procErr); err != nil {
		// Could not park: keep the message instead of losing it.
		c.Log.ErrorContext(ctx, "parking failed, requeueing", "queue", c.Queue, "error", err.Error())
		time.Sleep(c.RetryDelay)
		_ = d.Nack(false, true)
		return
	}
	c.Log.ErrorContext(ctx, "message parked in "+QueueDead, "queue", c.Queue,
		"attempt", attempt, "reason", reason, "error", procErr.Error())
	failedDeliveries.WithLabelValues(c.Queue, "park").Inc()
	_ = d.Ack(false)
}

// park copies the message to pulse.dlx (→ pulse.dead) with the reason in
// headers and waits for the broker's confirm.
func (c *Consumer) park(ctx context.Context, ch *amqp.Channel, d amqp.Delivery, reason string, procErr error) error {
	headers := maps.Clone(d.Headers)
	if headers == nil {
		headers = amqp.Table{}
	}
	headers["x-pulse-parked-from"] = c.Queue
	headers["x-pulse-parked-reason"] = reason
	headers["x-pulse-last-error"] = procErr.Error()

	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conf, err := ch.PublishWithDeferredConfirmWithContext(pctx, ExchangeDLX, d.RoutingKey, false, false, amqp.Publishing{
		Headers:      headers,
		ContentType:  d.ContentType,
		DeliveryMode: amqp.Persistent,
		MessageId:    d.MessageId,
		Body:         d.Body,
	})
	if err != nil {
		return fmt.Errorf("publish to %s: %w", ExchangeDLX, err)
	}
	ok, err := conf.WaitContext(pctx)
	if err != nil {
		return fmt.Errorf("wait for confirm: %w", err)
	}
	if !ok {
		return fmt.Errorf("broker nacked the parked message")
	}
	return nil
}
