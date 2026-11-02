// Package kafkax is the Kafka layer on github.com/twmb/franz-go: settings
// from KAFKA_* variables, client options (TLS, SASL SCRAM), topic creation
// and the traceparent header carrier.
package kafkax

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl/scram"

	"pulse/internal/config"
)

// Defaults from CONTRACT.md ("Kafka").
const (
	DefaultTopic = "pulse.check-results"
	DefaultGroup = "pulse-aggregator"

	TopicPartitions = 6
	TopicRetention  = 7 * 24 * time.Hour
)

// Supported KAFKA_SASL_MECHANISM values.
const (
	SASLScramSHA512 = "SCRAM-SHA-512" // what Strimzi issues for KafkaUser
	SASLScramSHA256 = "SCRAM-SHA-256"
)

// Config is read from KAFKA_* variables. Empty Brokers means Kafka is disabled.
type Config struct {
	Brokers  []string
	Topic    string
	ClientID string
	Group    string // only the aggregator uses it

	TLS           bool
	CAFile        string // empty: system roots
	SASLMechanism string // empty: no authentication
	Username      string
	Password      string

	AutoCreateTopic bool // only the checker uses it
}

// Enabled reports whether KAFKA_BROKERS is set.
func (c Config) Enabled() bool { return len(c.Brokers) > 0 }

// Load reads the variables. service is the default KAFKA_CLIENT_ID.
// Errors are recorded in env (see config.Env.Err).
func Load(env *config.Env, service string) Config {
	c := Config{
		Brokers:         splitList(env.String("KAFKA_BROKERS", "")),
		Topic:           env.String("KAFKA_TOPIC_CHECK_RESULTS", DefaultTopic),
		ClientID:        env.String("KAFKA_CLIENT_ID", service),
		Group:           env.String("KAFKA_GROUP", DefaultGroup),
		TLS:             env.Bool("KAFKA_TLS", false),
		CAFile:          env.String("KAFKA_CA_FILE", ""),
		SASLMechanism:   strings.ToUpper(env.String("KAFKA_SASL_MECHANISM", "")),
		Username:        env.String("KAFKA_USERNAME", ""),
		Password:        env.String("KAFKA_PASSWORD", ""), // KAFKA_PASSWORD_FILE works too
		AutoCreateTopic: env.Bool("KAFKA_AUTO_CREATE_TOPIC", true),
	}
	for _, err := range c.validate() {
		env.Fail(err)
	}
	return c
}

func (c Config) validate() []error {
	var errs []error
	switch c.SASLMechanism {
	case "":
		if c.Username != "" || c.Password != "" {
			errs = append(errs, errors.New("KAFKA_USERNAME/KAFKA_PASSWORD are set, but KAFKA_SASL_MECHANISM is empty"))
		}
	case SASLScramSHA512, SASLScramSHA256:
		if c.Username == "" || c.Password == "" {
			errs = append(errs, fmt.Errorf("KAFKA_SASL_MECHANISM=%s needs KAFKA_USERNAME and KAFKA_PASSWORD (or KAFKA_PASSWORD_FILE)", c.SASLMechanism))
		}
	default:
		errs = append(errs, fmt.Errorf("KAFKA_SASL_MECHANISM: %q is not supported (use %s or %s)",
			c.SASLMechanism, SASLScramSHA512, SASLScramSHA256))
	}
	if c.CAFile != "" && !c.TLS {
		errs = append(errs, errors.New("KAFKA_CA_FILE is set, but KAFKA_TLS is not true"))
	}
	for _, b := range c.Brokers {
		if strings.Contains(b, "://") {
			errs = append(errs, fmt.Errorf("KAFKA_BROKERS: %q must be host:port without a scheme", b))
		}
	}
	if c.Topic == "" {
		errs = append(errs, errors.New("KAFKA_TOPIC_CHECK_RESULTS is empty"))
	}
	return errs
}

// ClientOpts are the connection options shared by producer and consumer.
func (c Config) ClientOpts() ([]kgo.Opt, error) {
	opts := []kgo.Opt{
		kgo.SeedBrokers(c.Brokers...),
		kgo.ClientID(c.ClientID),
	}
	if c.TLS {
		tc := &tls.Config{MinVersion: tls.VersionTLS12}
		if c.CAFile != "" {
			pem, err := os.ReadFile(c.CAFile)
			if err != nil {
				return nil, fmt.Errorf("KAFKA_CA_FILE: %w", err)
			}
			tc.RootCAs = x509.NewCertPool()
			if !tc.RootCAs.AppendCertsFromPEM(pem) {
				return nil, fmt.Errorf("KAFKA_CA_FILE: %s has no PEM certificates", c.CAFile)
			}
		}
		opts = append(opts, kgo.DialTLSConfig(tc))
	}
	auth := scram.Auth{User: c.Username, Pass: c.Password}
	switch c.SASLMechanism {
	case SASLScramSHA512:
		opts = append(opts, kgo.SASL(auth.AsSha512Mechanism()))
	case SASLScramSHA256:
		opts = append(opts, kgo.SASL(auth.AsSha256Mechanism()))
	}
	return opts, nil
}

// ProducerOpts: acks=all and the idempotent producer (franz-go enables it by
// default; we only make sure nothing turns it off). With acks=all a record is
// confirmed only when all in-sync replicas have it, so a confirmed record
// survives the loss of the leader. deliveryTimeout bounds how long a record
// may wait while the brokers are unavailable.
func ProducerOpts(deliveryTimeout time.Duration) []kgo.Opt {
	return []kgo.Opt{
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.RecordDeliveryTimeout(deliveryTimeout),
		// No AllowAutoTopicCreation: producing to a missing topic must fail,
		// not create it with the broker defaults (1 partition, RF 1).
	}
}

func splitList(s string) []string {
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
