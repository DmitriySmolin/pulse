// scheduler publishes check.requested for monitors whose next_check_at has come.
package main

import (
	"context"
	"time"

	"pulse/internal/amqpx"
	"pulse/internal/app"
	"pulse/internal/config"
	"pulse/internal/postgres"
	"pulse/internal/scheduler"
)

func main() {
	env := config.FromOS()
	base := app.LoadBase(env, "scheduler", ":8081")
	var (
		databaseURL = env.Required("DATABASE_URL")
		amqpURL     = env.Required("AMQP_URL")
		declare     = env.Bool("AMQP_DECLARE_TOPOLOGY", true)
		retryCfg    = amqpx.LoadRetry(env) // only shapes the declared topology
		tick       = env.Duration("SCHEDULER_TICK", 5*time.Second)
		batch       = env.Int("SCHEDULER_BATCH", 100)
	)

	app.Run(env, base, func(ctx context.Context, a *app.App) error {
		startCtx, cancel := context.WithTimeout(ctx, app.StartupTimeout)
		defer cancel()

		db, err := postgres.Connect(startCtx, databaseURL, "pulse-scheduler", a.Log)
		if err != nil {
			return err
		}
		defer db.Close()

		mq, err := amqpx.Dial(startCtx, amqpx.Options{URL: amqpURL, Name: "pulse-scheduler", DeclareTopology: declare, Retry: retryCfg, Log: a.Log})
		if err != nil {
			return err
		}
		defer mq.Close()
		pub := amqpx.NewPublisher(mq)
		defer pub.Close()

		a.HTTP.AddReadyCheck("postgres", db.Ping)
		a.HTTP.AddReadyCheck("rabbitmq", mq.Check)
		a.HTTP.SetStarted()

		s := &scheduler.Scheduler{DB: db, Pub: pub, Tick: tick, Batch: batch, Log: a.Log}
		s.Run(ctx)
		return nil
	})
}
