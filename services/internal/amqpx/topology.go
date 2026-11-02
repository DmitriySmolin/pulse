// Package amqpx is the RabbitMQ layer: topology, a self-healing connection,
// a publisher with confirms and a consumer with a worker pool.
package amqpx

import (
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Names from CONTRACT.md, "RabbitMQ".
const (
	ExchangeChecks = "pulse.checks"
	ExchangeEvents = "pulse.events"
	ExchangeDLX    = "pulse.dlx"
	ExchangeRetry  = "pulse.retry" // only with AMQP_RETRY_MODE=delayed

	QueueChecks        = "pulse.checks.requested"
	QueueChecksRetry   = "pulse.checks.retry" // only with AMQP_RETRY_MODE=delayed
	QueueNotifications = "pulse.notifications"
	QueueDead          = "pulse.dead"

	KeyCheckRequested = "check.requested"
	KeyStatusChanged  = "status.changed"
)

// DeclareTopology is idempotent. If a queue already exists with different
// arguments (for example, the retry mode was switched), RabbitMQ answers
// PRECONDITION_FAILED and the channel is closed: delete the queue or manage
// the topology outside the services (AMQP_DECLARE_TOPOLOGY=false).
func DeclareTopology(ch *amqp.Channel, retry Retry) error {
	exchanges := []string{ExchangeChecks, ExchangeEvents, ExchangeDLX}
	checksDLX := ExchangeDLX
	if retry.Delayed {
		exchanges = append(exchanges, ExchangeRetry)
		checksDLX = ExchangeRetry
	}
	for _, ex := range exchanges {
		if err := ch.ExchangeDeclare(ex, amqp.ExchangeTopic, true, false, false, false, nil); err != nil {
			return fmt.Errorf("declare exchange %s: %w", ex, err)
		}
	}
	type queue struct {
		name, exchange, key string
		args                amqp.Table
	}
	queues := []queue{
		{QueueChecks, ExchangeChecks, KeyCheckRequested, amqp.Table{
			amqp.QueueTypeArg: amqp.QueueTypeQuorum, "x-dead-letter-exchange": checksDLX, "x-delivery-limit": 5}},
		// No explicit delivery limit: RabbitMQ 4 uses 20 for quorum queues by default.
		{QueueNotifications, ExchangeEvents, KeyStatusChanged, amqp.Table{
			amqp.QueueTypeArg: amqp.QueueTypeQuorum, "x-dead-letter-exchange": ExchangeDLX}},
		{QueueDead, ExchangeDLX, "#", amqp.Table{amqp.QueueTypeArg: amqp.QueueTypeQuorum}},
	}
	if retry.Delayed {
		// Messages wait here for the TTL and are dead-lettered back to
		// pulse.checks with their original routing key.
		queues = append(queues, queue{QueueChecksRetry, ExchangeRetry, "#", amqp.Table{
			amqp.QueueTypeArg:        amqp.QueueTypeQuorum,
			"x-message-ttl":          retry.Delay.Milliseconds(),
			"x-dead-letter-exchange": ExchangeChecks,
		}})
	}
	for _, q := range queues {
		if _, err := ch.QueueDeclare(q.name, true, false, false, false, q.args); err != nil {
			return fmt.Errorf("declare queue %s: %w", q.name, err)
		}
		if err := ch.QueueBind(q.name, q.key, q.exchange, false, nil); err != nil {
			return fmt.Errorf("bind queue %s to %s/%s: %w", q.name, q.exchange, q.key, err)
		}
	}
	return nil
}
