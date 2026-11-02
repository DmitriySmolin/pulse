package notifier

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"pulse/internal/amqpx"
	"pulse/internal/message"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

type memDedup struct{ keys map[string]bool }

func (m *memDedup) Acquire(_ context.Context, key string, _ time.Duration) (bool, error) {
	if m.keys[key] {
		return false, nil
	}
	m.keys[key] = true
	return true, nil
}

func (m *memDedup) Release(_ context.Context, key string) error {
	delete(m.keys, key)
	return nil
}

type fakeSender struct {
	sent []string
	fail bool
}

func (f *fakeSender) Name() string { return "fake" }

func (f *fakeSender) Send(_ context.Context, _ message.StatusChanged, text string) error {
	if f.fail {
		return errors.New("boom")
	}
	f.sent = append(f.sent, text)
	return nil
}

func delivery(t *testing.T, ev message.StatusChanged) amqp.Delivery {
	t.Helper()
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	return amqp.Delivery{Body: b}
}

func TestDedupKey(t *testing.T) {
	if got := DedupKey(12, "down"); got != "pulse:notify:12:down" {
		t.Errorf("DedupKey = %q", got)
	}
}

func TestHandleDeduplicates(t *testing.T) {
	dedup := &memDedup{keys: map[string]bool{}}
	sender := &fakeSender{}
	s := &Service{Dedup: dedup, Window: time.Minute, Sender: sender, Log: discard}

	down := message.StatusChanged{MonitorID: 1, MonitorName: "api", From: "up", To: "down"}
	for range 2 {
		if err := s.Handle(context.Background(), delivery(t, down)); err != nil {
			t.Fatal(err)
		}
	}
	up := message.StatusChanged{MonitorID: 1, MonitorName: "api", From: "down", To: "up"}
	if err := s.Handle(context.Background(), delivery(t, up)); err != nil {
		t.Fatal(err)
	}
	if len(sender.sent) != 2 {
		t.Fatalf("want down+up sent once each, got %d: %v", len(sender.sent), sender.sent)
	}
}

func TestHandleFailureReleasesKey(t *testing.T) {
	dedup := &memDedup{keys: map[string]bool{}}
	sender := &fakeSender{fail: true}
	s := &Service{Dedup: dedup, Window: time.Minute, Sender: sender, Log: discard}
	ev := message.StatusChanged{MonitorID: 2, From: "up", To: "down"}

	if err := s.Handle(context.Background(), delivery(t, ev)); err == nil || errors.Is(err, amqpx.ErrReject) {
		t.Fatalf("send failure must be a retryable error, got %v", err)
	}
	sender.fail = false
	if err := s.Handle(context.Background(), delivery(t, ev)); err != nil || len(sender.sent) != 1 {
		t.Fatalf("retry after failure must be sent, err=%v sent=%v", err, sender.sent)
	}
}

func TestHandleBadMessageIsRejected(t *testing.T) {
	s := &Service{Dedup: &memDedup{keys: map[string]bool{}}, Sender: &fakeSender{}, Log: discard}
	err := s.Handle(context.Background(), amqp.Delivery{Body: []byte("{")})
	if !errors.Is(err, amqpx.ErrReject) {
		t.Fatalf("want ErrReject, got %v", err)
	}
}

func TestFormatText(t *testing.T) {
	id := int64(7)
	got := FormatText(message.StatusChanged{MonitorID: 1, MonitorName: "target", URL: "http://target:8090/",
		From: "up", To: "down", At: time.Date(2026, 10, 12, 10, 0, 5, 0, time.UTC),
		Error: "timeout after 5000ms", IncidentID: &id})
	want := "[DOWN] target\nhttp://target:8090/\nup -> down at 2026-10-12T10:00:05Z\nerror: timeout after 5000ms\nincident #7"
	if got != want {
		t.Errorf("FormatText:\n%s\nwant:\n%s", got, want)
	}
}

func TestSelectSender(t *testing.T) {
	cases := []struct {
		cfg  Config
		want string
	}{
		{Config{TelegramToken: "t", TelegramChatID: "c", WebhookURL: "http://hook"}, "telegram"},
		{Config{WebhookURL: "http://hook"}, "webhook"},
		{Config{}, "log"},
	}
	for _, c := range cases {
		if got := SelectSender(c.cfg, discard).Name(); got != c.want {
			t.Errorf("SelectSender(%+v) = %s, want %s", c.cfg, got, c.want)
		}
	}
}

func TestTelegram(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		if gotBody["chat_id"] == "bad" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"ok":false,"description":"Bad Request: chat not found"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	tg := &Telegram{APIURL: srv.URL + "/", Token: "123:SECRET", ChatID: "42", Client: srv.Client()}
	if err := tg.Send(context.Background(), message.StatusChanged{}, "hello"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/bot123:SECRET/sendMessage" || gotBody["text"] != "hello" || gotBody["chat_id"] != "42" {
		t.Errorf("request path=%s body=%v", gotPath, gotBody)
	}

	tg.ChatID = "bad"
	if err := tg.Send(context.Background(), message.StatusChanged{}, "x"); err == nil ||
		!strings.Contains(err.Error(), "chat not found") {
		t.Errorf("want API error, got %v", err)
	}

	srv.Close()
	err := tg.Send(context.Background(), message.StatusChanged{}, "x")
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("transport error must not contain the token: %v", err)
	}
}

func TestWebhook(t *testing.T) {
	var got map[string]any
	code := http.StatusNoContent
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(code)
	}))
	defer srv.Close()

	wh := &Webhook{URL: srv.URL, Client: srv.Client()}
	ev := message.StatusChanged{MonitorID: 5, From: "up", To: "down"}
	if err := wh.Send(context.Background(), ev, "text"); err != nil {
		t.Fatal(err)
	}
	if got["monitor_id"] != float64(5) || got["to"] != "down" || got["text"] != "text" {
		t.Errorf("payload = %v", got)
	}
	code = http.StatusBadGateway
	if err := wh.Send(context.Background(), ev, "text"); err == nil {
		t.Error("want error on 502")
	}
}
