package amqpx

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
)

const publishTimeout = 5 * time.Second

// Publisher sends persistent JSON messages with publisher confirms.
// Publish returns nil only after the broker has taken responsibility for
// the message (basic.ack) and it was routed to at least one queue.
// Calls are serialized: one channel, one message in flight.
type Publisher struct {
	conn *Conn

	mu      sync.Mutex
	ch      *amqp.Channel
	returns chan amqp.Return
}

// NewPublisher opens its channel lazily on the first Publish.
func NewPublisher(c *Conn) *Publisher { return &Publisher{conn: c} }

// Publish injects the trace context from ctx into the traceparent header.
func (p *Publisher) Publish(ctx context.Context, exchange, key string, body []byte) error {
	ctx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()

	headers := amqp.Table{}
	otel.GetTextMapPropagator().Inject(ctx, HeaderCarrier(headers))

	p.mu.Lock()
	defer p.mu.Unlock()

	ch, err := p.channel(ctx)
	if err != nil {
		return err
	}
	for len(p.returns) > 0 { // drop stale returns, if any
		<-p.returns
	}

	// mandatory=true: if no queue is bound, the broker returns the message
	// instead of silently dropping it.
	confirm, err := ch.PublishWithDeferredConfirmWithContext(ctx, exchange, key, true, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Timestamp:    time.Now(),
		Headers:      headers,
		Body:         body,
	})
	if err != nil {
		p.reset()
		return fmt.Errorf("publish to %s/%s: %w", exchange, key, err)
	}
	acked, err := confirm.WaitContext(ctx)
	switch {
	case err != nil:
		p.reset()
		return fmt.Errorf("wait for publisher confirm: %w", err)
	case !acked && ch.IsClosed():
		p.reset()
		return errors.New("channel closed before the publisher confirm arrived")
	case !acked:
		return errors.New("broker nacked the message")
	}
	// A basic.return always arrives before the ack of the same message.
	select {
	case r := <-p.returns:
		return fmt.Errorf("message to %s/%s is unroutable (%d %s): is the queue declared and bound?",
			exchange, key, r.ReplyCode, r.ReplyText)
	default:
		return nil
	}
}

func (p *Publisher) channel(ctx context.Context) (*amqp.Channel, error) {
	if p.ch != nil && !p.ch.IsClosed() {
		return p.ch, nil
	}
	ch, err := p.conn.Channel(ctx)
	if err != nil {
		return nil, err
	}
	if err := ch.Confirm(false); err != nil {
		_ = ch.Close()
		return nil, fmt.Errorf("enable publisher confirms: %w", err)
	}
	p.returns = ch.NotifyReturn(make(chan amqp.Return, 16))
	p.ch = ch
	return ch, nil
}

func (p *Publisher) reset() {
	if p.ch != nil {
		_ = p.ch.Close()
		p.ch = nil
	}
}

// Close closes the channel.
func (p *Publisher) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reset()
}
