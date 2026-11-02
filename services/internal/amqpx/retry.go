package amqpx

import (
	"fmt"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"pulse/internal/config"
)

// Retry is how a failed check.requested is retried (CONTRACT.md, "RabbitMQ").
//
// requeue (default): nack with requeue after a fixed pause; after
// x-delivery-limit attempts RabbitMQ dead-letters the message to pulse.dead.
// A database outage longer than ~5 s burns all attempts.
//
// delayed: the message is rejected, dead-lettered to pulse.retry and waits
// in pulse.checks.retry for its TTL (AMQP_RETRY_DELAY), then returns to
// pulse.checks. After AMQP_RETRY_MAX failed attempts (counted from the
// x-death header) the consumer parks it in pulse.dead itself.
//
//	pulse.checks ─► pulse.checks.requested ──reject──► pulse.retry ─► pulse.checks.retry
//	     ▲                     │                                            │ TTL
//	     └─────────────────────┼──────────── dead-letter ◄──────────────────┘
//	                           └── attempts ≥ max ──► pulse.dlx ─► pulse.dead
type Retry struct {
	Delayed bool          // AMQP_RETRY_MODE=delayed
	Delay   time.Duration // AMQP_RETRY_DELAY: TTL of pulse.checks.retry
	Max     int           // AMQP_RETRY_MAX: failed attempts before parking
}

// LoadRetry reads AMQP_RETRY_*. Every service that declares the topology must
// use the same values, otherwise RabbitMQ answers PRECONDITION_FAILED.
func LoadRetry(env *config.Env) Retry {
	r := Retry{
		Delay: env.Duration("AMQP_RETRY_DELAY", 10*time.Second),
		Max:   env.Int("AMQP_RETRY_MAX", 5),
	}
	switch mode := env.String("AMQP_RETRY_MODE", "requeue"); mode {
	case "requeue":
	case "delayed":
		r.Delayed = true
	default:
		env.Fail(fmt.Errorf("AMQP_RETRY_MODE must be requeue or delayed, got %q", mode))
	}
	if r.Max < 1 {
		env.Fail(fmt.Errorf("AMQP_RETRY_MAX must be at least 1, got %d", r.Max))
	}
	if r.Delay < time.Second {
		env.Fail(fmt.Errorf("AMQP_RETRY_DELAY must be at least 1s, got %s", r.Delay))
	}
	return r
}

// deathCount returns how many times the message was dead-lettered from queue
// for reason (x-death is maintained by RabbitMQ, one entry per queue+reason).
func deathCount(headers amqp.Table, queue, reason string) int64 {
	deaths, ok := headers["x-death"].([]interface{})
	if !ok {
		return 0
	}
	for _, x := range deaths {
		t, ok := x.(amqp.Table)
		if !ok || t["queue"] != queue || t["reason"] != reason {
			continue
		}
		switch c := t["count"].(type) {
		case int64:
			return c
		case int32:
			return int64(c)
		case int:
			return int64(c)
		}
	}
	return 0
}
