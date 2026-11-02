package checker

import "testing"

func TestDecide(t *testing.T) {
	cases := []struct {
		name         string
		prev         string
		prevFailures int
		ok           bool
		want         Outcome
	}{
		{"first ok", "unknown", 0, true, Outcome{"up", 0, true, IncidentNone}},
		{"still up", "up", 0, true, Outcome{"up", 0, false, IncidentNone}},
		{"up, ok resets failures", "up", 1, true, Outcome{"up", 0, false, IncidentNone}},
		{"first fail below threshold", "up", 0, false, Outcome{"up", 1, false, IncidentNone}},
		{"reaches threshold", "up", 1, false, Outcome{"down", 2, true, IncidentOpen}},
		{"new monitor never worked", "unknown", 1, false, Outcome{"down", 2, true, IncidentOpen}},
		{"unknown below threshold", "unknown", 0, false, Outcome{"unknown", 1, false, IncidentNone}},
		{"still down", "down", 5, false, Outcome{"down", 6, false, IncidentNone}},
		{"recovered", "down", 6, true, Outcome{"up", 0, true, IncidentResolve}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Decide(c.prev, c.prevFailures, c.ok, 2); got != c.want {
				t.Errorf("Decide(%s, %d, %v) = %+v, want %+v", c.prev, c.prevFailures, c.ok, got, c.want)
			}
		})
	}
}

func TestDecideThresholdOne(t *testing.T) {
	if got := Decide("up", 0, false, 1); got.Status != "down" || got.Incident != IncidentOpen {
		t.Errorf("threshold 1: %+v", got)
	}
}
