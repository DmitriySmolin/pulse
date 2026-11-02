package amqpx

import (
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"pulse/internal/config"
)

func TestLoadRetry(t *testing.T) {
	r := LoadRetry(config.FromMap(nil))
	if r.Delayed || r.Delay != 10*time.Second || r.Max != 5 {
		t.Errorf("defaults: %+v", r)
	}

	env := config.FromMap(map[string]string{"AMQP_RETRY_MODE": "delayed", "AMQP_RETRY_DELAY": "30s", "AMQP_RETRY_MAX": "3"})
	r = LoadRetry(env)
	if err := env.Err(); err != nil || !r.Delayed || r.Delay != 30*time.Second || r.Max != 3 {
		t.Errorf("delayed: %+v, err %v", r, err)
	}

	for _, bad := range []map[string]string{
		{"AMQP_RETRY_MODE": "exponential"},
		{"AMQP_RETRY_MAX": "0"},
		{"AMQP_RETRY_DELAY": "100ms"},
	} {
		env := config.FromMap(bad)
		LoadRetry(env)
		if env.Err() == nil {
			t.Errorf("%v: expected a configuration error", bad)
		}
	}
}

func TestDeathCount(t *testing.T) {
	h := amqp.Table{"x-death": []interface{}{
		amqp.Table{"queue": QueueChecksRetry, "reason": "expired", "count": int64(2)},
		amqp.Table{"queue": QueueChecks, "reason": "rejected", "count": int64(2)},
		amqp.Table{"queue": QueueChecks, "reason": "delivery_limit", "count": int32(1)},
	}}
	if got := deathCount(h, QueueChecks, "rejected"); got != 2 {
		t.Errorf("rejected from main queue: %d, want 2", got)
	}
	if got := deathCount(h, QueueChecks, "delivery_limit"); got != 1 {
		t.Errorf("int32 count: %d, want 1", got)
	}
	if got := deathCount(amqp.Table{}, QueueChecks, "rejected"); got != 0 {
		t.Errorf("no header: %d, want 0", got)
	}
	if got := deathCount(amqp.Table{"x-death": "garbage"}, QueueChecks, "rejected"); got != 0 {
		t.Errorf("malformed header: %d, want 0", got)
	}
}
