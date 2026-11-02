// Package notifier turns status.changed events into alerts:
// Telegram, else webhook, else only a log line. Repeated alerts for the same
// monitor and status within NOTIFIER_DEDUP_WINDOW are dropped.
package notifier

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	amqp "github.com/rabbitmq/amqp091-go"

	"pulse/internal/amqpx"
	"pulse/internal/message"
)

var (
	notificationsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "pulse_notifier_notifications_total", Help: "Notification attempts by channel and result."},
		[]string{"channel", "result"})
	deduplicatedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "pulse_notifier_deduplicated_total", Help: "Notifications dropped as duplicates."})
)

// Sender delivers one alert.
type Sender interface {
	Name() string // channel label: telegram, webhook, log
	Send(ctx context.Context, ev message.StatusChanged, text string) error
}

// Deduper remembers sent alerts for a time window (Valkey SET NX EX).
type Deduper interface {
	// Acquire returns false if the key already exists.
	Acquire(ctx context.Context, key string, ttl time.Duration) (bool, error)
	Release(ctx context.Context, key string) error
}

// Service is the message handler.
type Service struct {
	Dedup  Deduper
	Window time.Duration
	Sender Sender
	Log    *slog.Logger
}

// DedupKey is pulse:notify:{monitor_id}:{to}.
func DedupKey(monitorID int64, to string) string {
	return fmt.Sprintf("pulse:notify:%d:%s", monitorID, to)
}

// InitMetrics exports zero values for the selected channel.
func InitMetrics(channel string) {
	notificationsTotal.WithLabelValues(channel, "ok")
	notificationsTotal.WithLabelValues(channel, "error")
}

// Handle is an amqpx.Handler: a send error → nack and requeue.
func (s *Service) Handle(ctx context.Context, d amqp.Delivery) error {
	ev, err := message.ParseStatusChanged(d.Body)
	if err != nil {
		return fmt.Errorf("%w: %v", amqpx.ErrReject, err)
	}
	log := s.Log.With("monitor_id", ev.MonitorID, "from", ev.From, "to", ev.To)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	key := DedupKey(ev.MonitorID, ev.To)
	fresh, err := s.Dedup.Acquire(ctx, key, s.Window)
	if err != nil {
		return fmt.Errorf("dedup in valkey: %w", err)
	}
	if !fresh {
		deduplicatedTotal.Inc()
		log.InfoContext(ctx, "notification deduplicated", "key", key, "window", s.Window.String())
		return nil
	}

	channel := s.Sender.Name()
	if err := s.Sender.Send(ctx, ev, FormatText(ev)); err != nil {
		notificationsTotal.WithLabelValues(channel, "error").Inc()
		// Free the key, otherwise the redelivered message would be dropped as a duplicate.
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		if rerr := s.Dedup.Release(releaseCtx, key); rerr != nil {
			log.WarnContext(ctx, "dedup key not released, the retry may be dropped", "key", key, "error", rerr.Error())
		}
		return fmt.Errorf("send via %s: %w", channel, err)
	}
	notificationsTotal.WithLabelValues(channel, "ok").Inc()
	log.InfoContext(ctx, "notification sent", "channel", channel, "incident_id", ev.IncidentID)
	return nil
}

// FormatText is the human-readable alert.
func FormatText(ev message.StatusChanged) string {
	name := ev.MonitorName
	if name == "" {
		name = fmt.Sprintf("monitor #%d", ev.MonitorID)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[%s] %s\n%s\n%s -> %s at %s", strings.ToUpper(ev.To), name, ev.URL,
		ev.From, ev.To, ev.At.UTC().Format(time.RFC3339))
	if ev.Error != "" {
		fmt.Fprintf(&b, "\nerror: %s", ev.Error)
	}
	if ev.IncidentID != nil {
		if ev.To == message.StatusUp {
			fmt.Fprintf(&b, "\nincident #%d resolved", *ev.IncidentID)
		} else {
			fmt.Fprintf(&b, "\nincident #%d", *ev.IncidentID)
		}
	}
	return b.String()
}
