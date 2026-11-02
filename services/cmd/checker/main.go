// checker consumes pulse.checks.requested, runs HTTP checks and records
// results; with KAFKA_BROKERS set it also relays check.result from the
// outbox to Kafka.
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"pulse/internal/amqpx"
	"pulse/internal/app"
	"pulse/internal/checker"
	"pulse/internal/config"
	"pulse/internal/kafkax"
	"pulse/internal/outbox"
	"pulse/internal/postgres"
)

func main() {
	env := config.FromOS()
	base := app.LoadBase(env, "checker", ":8082")
	var (
		databaseURL = env.Required("DATABASE_URL")
		amqpURL     = env.Required("AMQP_URL")
		declare     = env.Bool("AMQP_DECLARE_TOPOLOGY", true)
		retryCfg    = amqpx.LoadRetry(env)
		workers     = env.Int("CHECKER_WORKERS", 8)
		prefetch    = env.Int("CHECKER_PREFETCH", 16)
		threshold   = env.Int("CHECKER_FAILURE_THRESHOLD", 2)
		userAgent   = env.String("CHECKER_USER_AGENT", "PulseChecker/1.0")
		s3cfg       = checker.S3Config{
			Endpoint:        env.String("S3_ENDPOINT", ""),
			Region:          env.String("S3_REGION", "us-east-1"),
			Bucket:          env.String("S3_BUCKET", "pulse-snapshots"),
			AccessKeyID:     env.String("S3_ACCESS_KEY_ID", ""),
			SecretAccessKey: env.String("S3_SECRET_ACCESS_KEY", ""),
			ForcePathStyle:  env.Bool("S3_FORCE_PATH_STYLE", true),
		}
		kafka          = kafkax.Load(env, base.Service)
		outboxPoll     = env.Duration("OUTBOX_POLL_INTERVAL", time.Second)
		outboxBatch    = env.Int("OUTBOX_BATCH", 500)
		publishTimeout = 10 * time.Second
	)

	app.Run(env, base, func(ctx context.Context, a *app.App) error {
		startCtx, cancel := context.WithTimeout(ctx, app.StartupTimeout)
		defer cancel()

		db, err := postgres.Connect(startCtx, databaseURL, "pulse-checker", a.Log)
		if err != nil {
			return err
		}
		defer db.Close()

		mq, err := amqpx.Dial(startCtx, amqpx.Options{URL: amqpURL, Name: "pulse-checker", DeclareTopology: declare, Retry: retryCfg, Log: a.Log})
		if err != nil {
			return err
		}
		defer mq.Close()
		pub := amqpx.NewPublisher(mq)
		defer pub.Close()

		var snapshots *checker.Snapshots
		if s3cfg.Endpoint != "" {
			snapshots = checker.NewSnapshots(s3cfg)
			if err := snapshots.CheckBucket(startCtx); err != nil {
				a.Log.Warn("snapshot bucket is not accessible, snapshots will fail",
					"endpoint", s3cfg.Endpoint, "bucket", s3cfg.Bucket, "error", err.Error())
			} else {
				a.Log.Info("snapshots enabled", "endpoint", s3cfg.Endpoint, "bucket", s3cfg.Bucket)
			}
		}

		// Kafka is optional and is NOT a readiness dependency: while it is down,
		// check.result events wait in the outbox and the checks go on.
		var relayDone chan struct{}
		resultsTopic := ""
		if kafka.Enabled() {
			opts, err := kafka.ClientOpts()
			if err != nil {
				return err
			}
			kcl, err := kgo.NewClient(append(opts, kafkax.ProducerOpts(publishTimeout)...)...)
			if err != nil {
				return fmt.Errorf("kafka client: %w", err)
			}
			defer kcl.Close() // after the relay has stopped (defers run in reverse order)
			resultsTopic = kafka.Topic

			relay := &outbox.Relay{
				DB: db, Producer: kcl, Interval: outboxPoll, Batch: outboxBatch,
				Timeout: publishTimeout, Log: a.Log.With("component", "outbox-relay"),
			}
			if kafka.AutoCreateTopic {
				relay.EnsureTopic = func(ctx context.Context) error {
					return kafkax.EnsureTopic(ctx, kcl, kafka.Topic, relay.Log)
				}
			}
			relayDone = make(chan struct{})
			go func() { defer close(relayDone); relay.Run(ctx) }()
			a.Log.Info("kafka enabled: check results go through the outbox",
				"brokers", kafka.Brokers, "topic", kafka.Topic, "tls", kafka.TLS, "sasl", kafka.SASLMechanism)
		} else {
			a.Log.Info("kafka disabled (KAFKA_BROKERS is empty): outbox is not used")
		}

		a.HTTP.AddReadyCheck("postgres", db.Ping)
		a.HTTP.AddReadyCheck("rabbitmq", mq.Check)
		a.HTTP.SetStarted()

		svc := &checker.Service{
			DB: db, Pub: pub, Prober: checker.NewProber(userAgent),
			Snapshots: snapshots, Threshold: threshold, ResultsTopic: resultsTopic, Log: a.Log,
		}
		consumer := &amqpx.Consumer{
			Conn: mq, Queue: amqpx.QueueChecks, Prefetch: prefetch, Workers: workers,
			Handler: svc.Handle, Log: a.Log, RetryDelay: time.Second, Retry: retryCfg,
		}
		consumer.Run(ctx)
		if relayDone != nil {
			<-relayDone // the relay finishes its current batch
		}
		return nil
	})
}
