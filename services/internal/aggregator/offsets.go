package aggregator

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// offsets are the next offsets to commit, per topic and partition.
// The poll loop adds to it, rebalance callbacks take from it: hence the mutex.
type offsets struct {
	mu sync.Mutex
	m  map[string]map[int32]kgo.EpochOffset
}

// add remembers record offset+1 (the committed offset is the NEXT record to read).
func (o *offsets) add(records []*kgo.Record) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.m == nil {
		o.m = map[string]map[int32]kgo.EpochOffset{}
	}
	for _, r := range records {
		parts := o.m[r.Topic]
		if parts == nil {
			parts = map[int32]kgo.EpochOffset{}
			o.m[r.Topic] = parts
		}
		next := kgo.EpochOffset{Epoch: r.LeaderEpoch, Offset: r.Offset + 1}
		if cur, ok := parts[r.Partition]; !ok || cur.Less(next) {
			parts[r.Partition] = next
		}
	}
}

// take removes and returns offsets of the given partitions (all if only is nil).
func (o *offsets) take(only map[string][]int32) map[string]map[int32]kgo.EpochOffset {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := map[string]map[int32]kgo.EpochOffset{}
	for topic, parts := range o.m {
		for p, eo := range parts {
			if only != nil && !slices.Contains(only[topic], p) {
				continue
			}
			if out[topic] == nil {
				out[topic] = map[int32]kgo.EpochOffset{}
			}
			out[topic][p] = eo
			delete(parts, p)
		}
		if len(parts) == 0 {
			delete(o.m, topic)
		}
	}
	return out
}

// putBack returns offsets after a failed commit, unless newer ones arrived meanwhile.
func (o *offsets) putBack(back map[string]map[int32]kgo.EpochOffset) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.m == nil {
		o.m = map[string]map[int32]kgo.EpochOffset{}
	}
	for topic, parts := range back {
		if o.m[topic] == nil {
			o.m[topic] = map[int32]kgo.EpochOffset{}
		}
		for p, eo := range parts {
			if cur, ok := o.m[topic][p]; !ok || cur.Less(eo) {
				o.m[topic][p] = eo
			}
		}
	}
}

func count(m map[string]map[int32]kgo.EpochOffset) int {
	n := 0
	for _, parts := range m {
		n += len(parts)
	}
	return n
}

// commitPending commits saved offsets (only: these partitions; nil: all).
// CommitOffsetsSync cannot be cancelled by a rebalance, which is what we need
// in onRevoked. On failure the offsets are kept for the next attempt.
func (a *Aggregator) commitPending(ctx context.Context, only map[string][]int32) {
	toCommit := a.pending.take(only)
	if len(toCommit) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var commitErr error
	a.Client.CommitOffsetsSync(ctx, maps.Clone(toCommit),
		func(_ *kgo.Client, _ *kmsg.OffsetCommitRequest, resp *kmsg.OffsetCommitResponse, err error) {
			if err != nil {
				commitErr = err
				return
			}
			for _, t := range resp.Topics {
				for _, p := range t.Partitions {
					if e := kerr.ErrorForCode(p.ErrorCode); e != nil && commitErr == nil {
						commitErr = e
					}
				}
			}
		})
	if commitErr != nil {
		processingErrorsTotal.WithLabelValues("commit").Inc()
		a.Log.Warn("offset commit failed: these records will be re-delivered (duplicates are filtered)",
			"partitions", count(toCommit), "error", commitErr.Error())
		if !isFenced(commitErr) {
			a.pending.putBack(toCommit)
		}
		return
	}
	a.Log.Debug("offsets committed", "partitions", count(toCommit))
}

// isFenced: the member is no longer in the group (kicked after the session
// timeout); its offsets will never be accepted, retrying is pointless.
func isFenced(err error) bool {
	return errors.Is(err, kerr.IllegalGeneration) || errors.Is(err, kerr.UnknownMemberID) ||
		errors.Is(err, kerr.FencedInstanceID)
}

// onRevoked: partitions move to another member. Commit what we saved for
// them now, otherwise the new owner starts from the old offset (duplicates).
func (a *Aggregator) onRevoked(ctx context.Context, _ *kgo.Client, revoked map[string][]int32) {
	a.Log.Info("partitions revoked, committing processed offsets", "partitions", revoked)
	a.commitPending(ctx, revoked)
}

// onLost: we were kicked out of the group; a commit would be rejected.
// Forget the offsets: the new owner re-reads these records, aggregated_checks
// filters them.
func (a *Aggregator) onLost(_ context.Context, _ *kgo.Client, lost map[string][]int32) {
	dropped := a.pending.take(lost)
	a.Log.Warn("partitions lost (session expired?), uncommitted offsets dropped",
		"partitions", lost, "dropped", count(dropped))
}
