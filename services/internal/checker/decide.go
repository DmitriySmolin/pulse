package checker

import "pulse/internal/message"

// IncidentAction says what to do with incidents after a check.
type IncidentAction int

const (
	IncidentNone IncidentAction = iota
	IncidentOpen
	IncidentResolve
)

// Outcome is the new monitor state after one check.
type Outcome struct {
	Status   string
	Failures int // consecutive_failures
	Changed  bool
	Incident IncidentAction
}

// Decide applies a check result to the monitor state (CONTRACT.md, "Логика checker"):
//   - ok: status up, failures reset to 0;
//   - fail: failures+1; status becomes down once failures >= threshold,
//     otherwise it stays as it was;
//   - any change to down opens an incident (also unknown→down: a new monitor
//     that never worked is an outage too), down→up resolves it.
func Decide(prevStatus string, prevFailures int, ok bool, threshold int) Outcome {
	out := Outcome{Status: prevStatus}
	if ok {
		out.Status = message.StatusUp
	} else {
		out.Failures = prevFailures + 1
		if out.Failures >= threshold {
			out.Status = message.StatusDown
		}
	}
	out.Changed = out.Status != prevStatus
	switch {
	case out.Changed && out.Status == message.StatusDown:
		out.Incident = IncidentOpen
	case out.Changed && prevStatus == message.StatusDown:
		out.Incident = IncidentResolve
	}
	return out
}
