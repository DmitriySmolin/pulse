package amqpx

import amqp "github.com/rabbitmq/amqp091-go"

// HeaderCarrier lets the OTel propagator read and write the W3C traceparent
// in AMQP message headers.
type HeaderCarrier amqp.Table

func (c HeaderCarrier) Get(key string) string {
	v, _ := c[key].(string)
	return v
}

func (c HeaderCarrier) Set(key, value string) { c[key] = value }

func (c HeaderCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}
