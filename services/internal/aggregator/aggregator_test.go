package aggregator

import (
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"pulse/internal/message"
)

func TestDeltaOf(t *testing.T) {
	at := time.Date(2026, 10, 12, 10, 59, 59, 0, time.FixedZone("MSK", 3*3600))
	d := DeltaOf(message.CheckResult{CheckID: 1, MonitorID: 7, CheckedAt: at, OK: true, LatencyMS: 41})
	want := Delta{MonitorID: 7, Hour: time.Date(2026, 10, 12, 7, 0, 0, 0, time.UTC), OK: 1, LatencyMS: 41}
	if d.MonitorID != want.MonitorID || !d.Hour.Equal(want.Hour) || d.OK != 1 || d.LatencyMS != 41 {
		t.Errorf("delta = %+v, want %+v", d, want)
	}
	if d.Hour.Location() != time.UTC {
		t.Errorf("hour must be in UTC, got %v", d.Hour.Location())
	}
	if DeltaOf(message.CheckResult{CheckedAt: at, OK: false}).OK != 0 {
		t.Error("failed check must add ok=0")
	}
}

func rec(topic string, partition int32, offset int64) *kgo.Record {
	return &kgo.Record{Topic: topic, Partition: partition, Offset: offset, LeaderEpoch: 1}
}

func TestOffsetsKeepNextOffsetPerPartition(t *testing.T) {
	var o offsets
	o.add([]*kgo.Record{rec("t", 0, 4), rec("t", 0, 9), rec("t", 1, 2)})
	o.add([]*kgo.Record{rec("t", 0, 7)}) // older than 9: must not rewind

	got := o.take(map[string][]int32{"t": {0}})
	if got["t"][0].Offset != 10 || len(got["t"]) != 1 {
		t.Fatalf("take(p0) = %+v, want offset 10 (last record + 1)", got)
	}
	rest := o.take(nil)
	if rest["t"][1].Offset != 3 || len(rest["t"]) != 1 {
		t.Fatalf("take(all) = %+v", rest)
	}
	if len(o.take(nil)) != 0 {
		t.Error("offsets must be empty after take")
	}
}

func TestOffsetsPutBackDoesNotRewind(t *testing.T) {
	var o offsets
	o.add([]*kgo.Record{rec("t", 0, 9)})
	failed := o.take(nil) // commit failed for offset 10 ...
	o.add([]*kgo.Record{rec("t", 0, 20)})
	o.putBack(failed) // ... while 21 was already saved
	if got := o.take(nil); got["t"][0].Offset != 21 {
		t.Errorf("after putBack offset = %d, want 21", got["t"][0].Offset)
	}
}
