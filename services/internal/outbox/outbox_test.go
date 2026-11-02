package outbox

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

func TestBuildRecords(t *testing.T) {
	batch := []row{
		{ID: 10, Topic: "pulse.check-results", Key: "1", Payload: `{"check_id": 5}`,
			Headers: `{"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"}`},
		{ID: 11, Topic: "pulse.check-results", Key: "2", Payload: `{"check_id": 6}`, Headers: `{}`},
	}
	recs, err := buildRecords(batch)
	if err != nil {
		t.Fatal(err)
	}
	r := recs[0]
	if r.Topic != "pulse.check-results" || string(r.Key) != "1" || string(r.Value) != `{"check_id": 5}` {
		t.Errorf("record = %+v", r)
	}
	if len(r.Headers) != 1 || r.Headers[0].Key != "traceparent" {
		t.Errorf("headers = %+v", r.Headers)
	}
	if len(recs[1].Headers) != 0 {
		t.Errorf("empty headers expected, got %+v", recs[1].Headers)
	}

	if _, err := buildRecords([]row{{ID: 1, Headers: "not json"}}); err == nil {
		t.Error("expected error for broken headers")
	}
}

func TestSplitResults(t *testing.T) {
	batch := []row{{ID: 1}, {ID: 2}, {ID: 3}}
	recs, _ := buildRecords([]row{{Headers: "{}"}, {Headers: "{}"}, {Headers: "{}"}})
	boom := errors.New("broker unavailable")
	// Results come back in any order.
	results := kgo.ProduceResults{
		{Record: recs[2], Err: boom},
		{Record: recs[0]},
		{Record: recs[1]},
	}
	done, failed, err := splitResults(batch, recs, results)
	slices.Sort(done)
	if !slices.Equal(done, []int64{1, 2}) || failed != 1 || !errors.Is(err, boom) {
		t.Errorf("done=%v failed=%d err=%v", done, failed, err)
	}

	// A record without a result is not confirmed and must stay in the outbox.
	done, failed, _ = splitResults(batch, recs, results[1:2])
	if !slices.Equal(done, []int64{1}) || failed != 2 {
		t.Errorf("missing results: done=%v failed=%d", done, failed)
	}
}

func TestNextBackoff(t *testing.T) {
	base := time.Second
	var got []time.Duration
	w := time.Duration(0)
	for range 7 {
		w = nextBackoff(w, base)
		got = append(got, w)
	}
	want := []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		16 * time.Second, 30 * time.Second, 30 * time.Second}
	if !slices.Equal(got, want) {
		t.Errorf("backoff = %v, want %v", got, want)
	}
}
