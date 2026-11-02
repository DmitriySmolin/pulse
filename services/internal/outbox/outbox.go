// Package outbox is the transactional outbox (CONTRACT.md, "Kafka").
//
// Problem: the checker must save a check to PostgreSQL AND publish it to
// Kafka. Two systems cannot be committed atomically: publish first and crash
// before the commit — Kafka has a check that does not exist; commit first and
// crash before publishing — the event is lost.
//
// Solution: the event is written to the outbox table in the same transaction
// as the check (Add). A separate relay (Relay) reads the outbox, publishes to
// Kafka, waits for acks=all and deletes the published rows. A crash between
// the publish and the delete only produces a duplicate, which the consumer
// filters out (aggregated_checks). So the guarantee is at-least-once, and
// Kafka being down never stops the checks: rows just pile up in the outbox.
package outbox

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Message is one outbox row.
type Message struct {
	Topic   string
	Key     string
	Payload any               // marshalled to JSON
	Headers map[string]string // e.g. traceparent
}

// Add inserts the message inside the caller's transaction.
func Add(ctx context.Context, tx pgx.Tx, m Message) error {
	payload, err := json.Marshal(m.Payload)
	if err != nil {
		return fmt.Errorf("outbox: marshal payload: %w", err)
	}
	headers, err := json.Marshal(m.Headers)
	if err != nil {
		return fmt.Errorf("outbox: marshal headers: %w", err)
	}
	if m.Headers == nil {
		headers = []byte("{}")
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO outbox (topic, msg_key, payload, headers) VALUES ($1, $2, $3::jsonb, $4::jsonb)`,
		m.Topic, m.Key, string(payload), string(headers))
	if err != nil {
		return fmt.Errorf("outbox: insert: %w", err)
	}
	return nil
}
