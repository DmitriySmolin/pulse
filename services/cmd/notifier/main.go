// notifier consumes pulse.notifications and sends alerts with deduplication.
package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"pulse/internal/amqpx"
	"pulse/internal/app"
	"pulse/internal/config"
	"pulse/internal/notifier"
	"pulse/internal/retry"
)

func main() {
	env := config.FromOS()
	base := app.LoadBase(env, "notifier", ":8083")
	var (
		amqpURL   = env.Required("AMQP_URL")
		declare   = env.Bool("AMQP_DECLARE_TOPOLOGY", true)
		retryCfg  = amqpx.LoadRetry(env) // only shapes the declared topology
		valkeyURL = env.Required("VALKEY_URL")
		window    = env.Duration("NOTIFIER_DEDUP_WINDOW", 10*time.Minute)
		channels  = notifier.Config{
			TelegramToken:  env.String("TELEGRAM_BOT_TOKEN", ""),
			TelegramChatID: env.String("TELEGRAM_CHAT_ID", ""),
			TelegramAPIURL: env.String("TELEGRAM_API_URL", "https://api.telegram.org"),
			WebhookURL:     env.String("WEBHOOK_URL", ""),
		}
	)
	if (channels.TelegramToken == "") != (channels.TelegramChatID == "") {
		env.Fail(errors.New("TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID must be set together"))
	}
	var valkeyOpts *redis.Options
	if valkeyURL != "" {
		var err error
		if valkeyOpts, err = redis.ParseURL(valkeyURL); err != nil {
			env.Fail(fmt.Errorf("VALKEY_URL: %w", err))
		}
	}

	app.Run(env, base, func(ctx context.Context, a *app.App) error {
		startCtx, cancel := context.WithTimeout(ctx, app.StartupTimeout)
		defer cancel()

		valkeyOpts.ClientName = "pulse-notifier"
		rdb := redis.NewClient(valkeyOpts)
		defer rdb.Close()
		ping := func(ctx context.Context) error { return rdb.Ping(ctx).Err() }
		if err := retry.Startup(startCtx, a.Log, "valkey", ping); err != nil {
			return err
		}
		a.Log.Info("connected to valkey", "addr", valkeyOpts.Addr)

		mq, err := amqpx.Dial(startCtx, amqpx.Options{URL: amqpURL, Name: "pulse-notifier", DeclareTopology: declare, Retry: retryCfg, Log: a.Log})
		if err != nil {
			return err
		}
		defer mq.Close()

		a.HTTP.AddReadyCheck("rabbitmq", mq.Check)
		a.HTTP.AddReadyCheck("valkey", ping)
		a.HTTP.SetStarted()

		sender := notifier.SelectSender(channels, a.Log)
		notifier.InitMetrics(sender.Name())
		a.Log.Info("notification channel selected", "channel", sender.Name(), "dedup_window", window.String())

		svc := &notifier.Service{Dedup: &notifier.Valkey{Client: rdb}, Window: window, Sender: sender, Log: a.Log}
		// One worker keeps events of a monitor in order (down, then up).
		consumer := &amqpx.Consumer{
			Conn: mq, Queue: amqpx.QueueNotifications, Prefetch: 10, Workers: 1,
			Handler: svc.Handle, Log: a.Log, RetryDelay: 2 * time.Second,
		}
		consumer.Run(ctx)
		return nil
	})
}
