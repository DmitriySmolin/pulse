package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"pulse/internal/message"
)

// Config selects the channel.
type Config struct {
	TelegramToken  string
	TelegramChatID string
	TelegramAPIURL string
	WebhookURL     string
}

// SelectSender applies the priority Telegram → webhook → log.
func SelectSender(cfg Config, log *slog.Logger) Sender {
	client := &http.Client{Timeout: 10 * time.Second}
	switch {
	case cfg.TelegramToken != "" && cfg.TelegramChatID != "":
		return &Telegram{APIURL: cfg.TelegramAPIURL, Token: cfg.TelegramToken, ChatID: cfg.TelegramChatID, Client: client}
	case cfg.WebhookURL != "":
		return &Webhook{URL: cfg.WebhookURL, Client: client}
	}
	return &LogSender{Log: log}
}

// Telegram calls the Bot API sendMessage.
type Telegram struct {
	APIURL, Token, ChatID string
	Client                *http.Client
}

func (t *Telegram) Name() string { return "telegram" }

func (t *Telegram) Send(ctx context.Context, _ message.StatusChanged, text string) error {
	body, _ := json.Marshal(map[string]any{
		"chat_id": t.ChatID, "text": text, "disable_web_page_preview": true,
	})
	// The token is part of the URL: never let it into logs or errors.
	endpoint := strings.TrimRight(t.APIURL, "/") + "/bot" + t.Token + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("telegram: cannot build request (check TELEGRAM_API_URL)")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.Client.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: %w", stripURL(err))
	}
	defer resp.Body.Close()

	var answer struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&answer)
	if resp.StatusCode != http.StatusOK || !answer.OK {
		return fmt.Errorf("telegram: HTTP %d: %s", resp.StatusCode, answer.Description)
	}
	return nil
}

// Webhook POSTs the event as JSON with an extra "text" field.
type Webhook struct {
	URL    string
	Client *http.Client
}

func (w *Webhook) Name() string { return "webhook" }

func (w *Webhook) Send(ctx context.Context, ev message.StatusChanged, text string) error {
	body, err := json.Marshal(struct {
		message.StatusChanged
		Text string `json:"text"`
	}{ev, text})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("webhook: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.Client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook: %w", stripURL(err)) // the URL may contain a secret too
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("webhook: HTTP %d", resp.StatusCode)
	}
	return nil
}

// LogSender only writes the alert to the log (no channel configured).
type LogSender struct{ Log *slog.Logger }

func (l *LogSender) Name() string { return "log" }

func (l *LogSender) Send(ctx context.Context, ev message.StatusChanged, text string) error {
	l.Log.InfoContext(ctx, "alert", "monitor_id", ev.MonitorID, "monitor_name", ev.MonitorName,
		"from", ev.From, "to", ev.To, "text", text)
	return nil
}

func stripURL(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}

// Valkey implements Deduper.
type Valkey struct{ Client *redis.Client }

func (v *Valkey) Acquire(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	return v.Client.SetNX(ctx, key, time.Now().UTC().Format(time.RFC3339), ttl).Result()
}

func (v *Valkey) Release(ctx context.Context, key string) error {
	return v.Client.Del(ctx, key).Err()
}
