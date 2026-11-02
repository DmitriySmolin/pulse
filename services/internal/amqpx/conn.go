package amqpx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"pulse/internal/retry"
)

// ErrClosed is returned after Conn.Close.
var ErrClosed = errors.New("amqp connection is closed")

// Options configure Dial.
type Options struct {
	URL             string
	Name            string // connection_name, visible in the RabbitMQ management UI
	DeclareTopology bool
	Retry           Retry // shapes the declared topology (AMQP_RETRY_MODE)
	Log             *slog.Logger
}

// Conn keeps one AMQP connection alive: when the broker drops it, a
// background goroutine re-dials with backoff and re-declares the topology.
// Publishers and consumers open channels through Channel and simply retry.
type Conn struct {
	opts Options

	mu   sync.Mutex
	conn *amqp.Connection

	done chan struct{}
	once sync.Once
}

// Dial connects with startup retries (bounded by ctx) and starts the supervisor.
func Dial(ctx context.Context, opts Options) (*Conn, error) {
	c := &Conn{opts: opts, done: make(chan struct{})}
	if err := retry.Startup(ctx, opts.Log, "rabbitmq", c.connect); err != nil {
		return nil, err
	}
	opts.Log.Info("connected to rabbitmq", "topology_declared", opts.DeclareTopology)
	go c.supervise()
	return c, nil
}

func (c *Conn) connect(ctx context.Context) error {
	timeout := 5 * time.Second
	if dl, ok := ctx.Deadline(); ok {
		timeout = max(time.Until(dl), 100*time.Millisecond)
	}
	conn, err := amqp.DialConfig(c.opts.URL, amqp.Config{
		Heartbeat:  10 * time.Second,
		Dial:       amqp.DefaultDial(timeout),
		Properties: amqp.Table{"connection_name": c.opts.Name},
	})
	if err != nil {
		return err
	}
	if c.opts.DeclareTopology {
		if err := declareOn(conn, c.opts.Retry); err != nil {
			_ = conn.Close()
			return err
		}
	}
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	return nil
}

func declareOn(conn *amqp.Connection, retry Retry) error {
	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	defer ch.Close()
	return DeclareTopology(ch, retry)
}

// supervise waits for the connection to drop and re-dials it.
func (c *Conn) supervise() {
	for {
		c.mu.Lock()
		conn := c.conn
		c.mu.Unlock()

		closed := conn.NotifyClose(make(chan *amqp.Error, 1))
		select {
		case <-c.done:
			return
		case amqpErr := <-closed:
			c.opts.Log.Warn("rabbitmq connection lost, reconnecting", "error", fmt.Sprint(amqpErr))
		}

		for delay := time.Second; ; delay = min(delay*2, 30*time.Second) {
			select {
			case <-c.done:
				return
			case <-time.After(delay):
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err := c.connect(ctx)
			cancel()
			if err == nil {
				c.opts.Log.Info("rabbitmq reconnected")
				break
			}
			c.opts.Log.Warn("rabbitmq reconnect failed", "retry_in", min(delay*2, 30*time.Second).String(), "error", err.Error())
		}
	}
}

// Channel opens a new channel, waiting while the connection is being restored.
func (c *Conn) Channel(ctx context.Context) (*amqp.Channel, error) {
	var lastErr error = errors.New("connection is down")
	for {
		c.mu.Lock()
		conn := c.conn
		c.mu.Unlock()
		if conn != nil && !conn.IsClosed() {
			ch, err := conn.Channel()
			if err == nil {
				return ch, nil
			}
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("open amqp channel: %w (%w)", ctx.Err(), lastErr)
		case <-c.done:
			return nil, ErrClosed
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// Check is the /readyz probe.
func (c *Conn) Check(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil || c.conn.IsClosed() {
		return errors.New("connection is down, reconnecting")
	}
	return nil
}

// Close stops reconnecting and closes the connection.
func (c *Conn) Close() {
	c.once.Do(func() {
		close(c.done)
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.conn != nil {
			_ = c.conn.Close()
		}
	})
}
