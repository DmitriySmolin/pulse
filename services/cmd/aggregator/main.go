// aggregator consumes check.result from Kafka (consumer group pulse-aggregator)
// and maintains uptime_hourly in PostgreSQL.
package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/twmb/franz-go/pkg/kgo"

	"pulse/internal/aggregator"
	"pulse/internal/app"
	"pulse/internal/config"
	"pulse/internal/kafkax"
	"pulse/internal/postgres"
	"pulse/internal/retry"
)

func main() {
	env := config.FromOS()
	base := app.LoadBase(env, "aggregator", ":8084")
	var (
		databaseURL = env.Required("DATABASE_URL")
		kafka       = kafkax.Load(env, base.Service)
	)
	if !kafka.Enabled() {
		env.Fail(errors.New("KAFKA_BROKERS is required: the aggregator only reads from Kafka " +
			"(without Kafka do not run it at all)"))
	}

	app.Run(env, base, func(ctx context.Context, a *app.App) error {
		startCtx, cancel := context.WithTimeout(ctx, app.StartupTimeout)
		defer cancel()

		db, err := postgres.Connect(startCtx, databaseURL, "pulse-aggregator", a.Log)
		if err != nil {
			return err
		}
		defer db.Close()

		agg := &aggregator.Aggregator{DB: db, MaxPoll: 500, Log: a.Log}
		opts, err := kafka.ClientOpts()
		if err != nil {
			return err
		}
		cl, err := kgo.NewClient(append(opts, agg.GroupOpts(kafka.Group, kafka.Topic)...)...)
		if err != nil {
			return fmt.Errorf("kafka client: %w", err)
		}
		agg.Client = cl
		if err := retry.Startup(startCtx, a.Log, "kafka", cl.Ping); err != nil {
			cl.Close()
			return err
		}
		a.Log.Info("connected to kafka", "brokers", kafka.Brokers, "topic", kafka.Topic, "group", kafka.Group,
			"tls", kafka.TLS, "sasl", kafka.SASLMechanism)

		a.HTTP.AddReadyCheck("postgres", db.Ping)
		a.HTTP.AddReadyCheck("kafka", cl.Ping)
		a.HTTP.SetStarted()

		agg.Run(ctx) // returns after the current batch is saved and committed
		agg.Close()  // commit what is left, leave the group (partitions move to other replicas at once)
		return nil
	})
}
