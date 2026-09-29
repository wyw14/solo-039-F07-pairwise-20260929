package relief

import (
	"context"
	"testing"
	"time"

	"github.com/wyw14/solo-039/internal/model"
)

func traceFor(campaign model.Campaign, obs Observations) Trace {
	commandAt := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	readings := make([]PressureReading, 0, len(obs.Pressures))
	for i, sample := range obs.Pressures {
		at := sample.At
		if at.IsZero() {
			at = commandAt.Add(time.Duration(i+1) * time.Second)
		}
		readings = append(readings, PressureReading{At: at, Pressure: sample.Value})
	}
	trace := Trace{
		Campaign:     campaign,
		Valve:        "flare",
		CommandAt:    commandAt,
		DeadlineAt:   commandAt.Add(30 * time.Second),
		SafePressure: DefaultSafePressure,
		Readings:     readings,
		EndedAt:      commandAt.Add(30 * time.Second),
	}
	switch {
	case obs.TimedOut:
		trace.TimedOut = true
	case obs.Response > 0:
		trace.ConfirmedAt = commandAt.Add(obs.Response)
	default:
		trace.ConfirmedAt = commandAt.Add(2 * time.Second)
	}
	return trace
}

func pressures(values ...float64) []PressureSample {
	out := make([]PressureSample, 0, len(values))
	for _, value := range values {
		out = append(out, PressureSample{Value: value})
	}
	return out
}

// A timeout followed by a successful retry must keep the first failure on the
// record: retries append, they never overwrite.
func TestHistoryRetryKeepsEarlierFailure(t *testing.T) {
	campaign := model.NewCampaign("baseline")
	history := NewHistory()
	history.BeginSession(campaign, "flare", DefaultSafePressure)

	first := history.Record(traceFor(campaign, Observations{TimedOut: true}))
	if first.Outcome != OutcomeTimedOut || !first.Failed() {
		t.Fatalf("first attempt = %s, want timed-out failure", first.Outcome)
	}
	if first.Index != 1 {
		t.Fatalf("first index = %d, want 1", first.Index)
	}

	second := history.Record(traceFor(campaign, Observations{Pressures: pressures(0.9, 0.4, 0.05)}))
	if second.Outcome != OutcomeDepressurized || second.Failed() {
		t.Fatalf("second attempt = %s, want depressurized success", second.Outcome)
	}
	if second.Index != 2 {
		t.Fatalf("second index = %d, want 2", second.Index)
	}

	sessions := history.Sessions()
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want one shutdown process", len(sessions))
	}
	session := sessions[0]
	if len(session.Attempts) != 2 {
		t.Fatalf("attempts = %d, want both original attempts preserved", len(session.Attempts))
	}
	if !session.Resolved || !session.Succeeded {
		t.Fatalf("session resolved=%v succeeded=%v, want both true", session.Resolved, session.Succeeded)
	}
	if session.Attempts[0].Outcome != OutcomeTimedOut {
		t.Fatalf("earlier failure was overwritten: %s", session.Attempts[0].Outcome)
	}
	if failures := history.Failures(); len(failures) != 1 || failures[0].Index != 1 {
		t.Fatalf("failures = %+v, want only the original timeout", failures)
	}
	if report := history.Report(); report.TotalAttempts != 2 || report.FailedAttempts != 1 || report.OpenProcesses != 0 {
		t.Fatalf("report = %+v", report)
	}
}

// No pressure reading is "not yet measured"; a high reading is a confirmed
// failure to depressurize. The two must never be conflated.
func TestHistoryDistinguishesNotMeasuredFromHeldPressure(t *testing.T) {
	campaign := model.NewCampaign("baseline")
	history := NewHistory()

	notMeasured := history.Record(traceFor(campaign, Observations{}))
	if notMeasured.Outcome != OutcomeNotMeasured {
		t.Fatalf("outcome = %s, want not-measured", notMeasured.Outcome)
	}
	if notMeasured.Failed() || notMeasured.Error() != nil {
		t.Fatalf("not-measured must not be a confirmed failure")
	}
	if notMeasured.PressureObserved {
		t.Fatalf("pressure must not be marked observed without a reading")
	}

	held := history.Record(traceFor(campaign, Observations{Pressures: pressures(0.9, 0.8)}))
	if held.Outcome != OutcomeHeldPressure || !held.Failed() {
		t.Fatalf("outcome = %s, want pressure-held failure", held.Outcome)
	}
	if held.Error() == nil {
		t.Fatalf("held pressure must carry a conclusion error")
	}

	// Domain evaluation agrees: absence of a reading is distinct from a high one.
	notMeasuredEval := Evaluate(model.Relief{Valve: "flare", Released: true}, DefaultSafePressure)
	if notMeasuredEval.PressureObserved || notMeasuredEval.Safe() || notMeasuredEval.Message != "depressurization not yet measured" {
		t.Fatalf("unexpected evaluation without readings: %+v", notMeasuredEval)
	}
	heldEval := Evaluate(model.Relief{Valve: "flare", Released: true, PressureObserved: true, Pressure: 0.8}, DefaultSafePressure)
	if heldEval.PressureSafe || heldEval.Safe() || heldEval.Message != "pressure confirmed above safe threshold" {
		t.Fatalf("unexpected evaluation with high reading: %+v", heldEval)
	}
}

// A valve that confirms late is flagged as slow even though it never timed out.
func TestHistoryFlagsSlowValveResponse(t *testing.T) {
	campaign := model.NewCampaign("baseline")
	history := NewHistoryWithSlowResponse(5 * time.Second)

	slow := traceFor(campaign, Observations{Response: 20 * time.Second, Pressures: pressures(0.05)})
	attempt := history.Record(slow)
	if !attempt.ResponseSlow || attempt.ResponseMS != 20000 {
		t.Fatalf("response_slow=%v response_ms=%d, want slow 20000ms", attempt.ResponseSlow, attempt.ResponseMS)
	}
	if attempt.Outcome != OutcomeDepressurized {
		t.Fatalf("slow valve that still confirmed is a success, got %s", attempt.Outcome)
	}

	fast := traceFor(campaign, Observations{Response: time.Second, Pressures: pressures(0.05)})
	fastAttempt := history.Record(fast)
	if fastAttempt.ResponseSlow {
		t.Fatalf("1s response must not be flagged slow")
	}
}

// Pressure must show sustained decline; readings that stall break the trend.
func TestHistoryPressureTrend(t *testing.T) {
	campaign := model.NewCampaign("baseline")
	history := NewHistory()

	declining := history.Record(traceFor(campaign, Observations{Pressures: pressures(0.9, 0.5, 0.2)}))
	if !declining.PressureObserved || !declining.PressureDeclining {
		t.Fatalf("want observed, strictly declining readings")
	}
	if declining.Outcome != OutcomeHeldPressure {
		t.Fatalf("still above threshold = pressure-held, got %s", declining.Outcome)
	}
	if declining.Conclusion != "pressure still falling but remains above safe threshold" {
		t.Fatalf("conclusion = %q", declining.Conclusion)
	}

	stalled := history.Record(traceFor(campaign, Observations{Pressures: pressures(0.5, 0.5)}))
	if stalled.PressureDeclining {
		t.Fatalf("equal readings are not a decline")
	}
	if stalled.Conclusion != "pressure confirmed above safe threshold with no sustained decline" {
		t.Fatalf("conclusion = %q", stalled.Conclusion)
	}

	safe := history.Record(traceFor(campaign, Observations{Pressures: pressures(0.5, 0.1)}))
	if safe.Outcome != OutcomeDepressurized || safe.PressureDelta >= 0 {
		t.Fatalf("want depressurized with negative delta, got %s delta=%v", safe.Outcome, safe.PressureDelta)
	}
}

// Each shutdown process is grouped by campaign; a new campaign starts a new
// process and an abandoned open process is closed rather than left dangling.
func TestHistoryGroupsSessionsByCampaign(t *testing.T) {
	first := model.NewCampaign("baseline")
	history := NewHistory()
	history.BeginSession(first, "flare", DefaultSafePressure)
	history.Record(traceFor(first, Observations{TimedOut: true}))

	second := model.NewCampaign("baseline")
	history.BeginSession(second, "flare", DefaultSafePressure)
	history.Record(traceFor(second, Observations{Pressures: pressures(0.05)}))

	sessions := history.Sessions()
	if len(sessions) != 2 {
		t.Fatalf("sessions = %d, want two shutdown processes", len(sessions))
	}
	if sessions[0].CampaignID == sessions[1].CampaignID {
		t.Fatalf("sessions must belong to different campaigns")
	}
	if !sessions[0].Resolved || sessions[0].Succeeded || sessions[0].FinalOutcome != OutcomeTimedOut {
		t.Fatalf("abandoned process = %+v", sessions[0])
	}
	if !sessions[1].Succeeded {
		t.Fatalf("second process should succeed")
	}
}

// Record without an explicit BeginSession still works (backwards compatible).
func TestHistoryRecordWithoutBegin(t *testing.T) {
	campaign := model.NewCampaign("baseline")
	history := NewHistory()
	attempt := history.Record(traceFor(campaign, Observations{TimedOut: true}))
	if attempt.Outcome != OutcomeTimedOut {
		t.Fatalf("outcome = %s", attempt.Outcome)
	}
	if _, ok := history.SessionForCampaign(campaign.ID); !ok {
		t.Fatalf("session should be opened lazily")
	}
}

// The coordinator must thread command, valve position and pressure readings
// into the trace and update the live state consistently.
func TestCoordinatorShutdownDetailed(t *testing.T) {
	campaign := model.NewCampaign("baseline")
	coordinator := NewCoordinator()

	trace := coordinator.ShutdownDetailed(context.Background(), campaign, "flare",
		Observations{Response: 3 * time.Second, Pressures: pressures(0.9, 0.05)})
	if trace.TimedOut || trace.ConfirmedAt.IsZero() {
		t.Fatalf("trace = %+v", trace)
	}
	if len(trace.Readings) != 2 {
		t.Fatalf("readings = %d, want 2", len(trace.Readings))
	}
	snapshot := coordinator.State.Snapshot()
	if !snapshot.Released || !snapshot.PressureObserved || snapshot.Pressure != 0.05 {
		t.Fatalf("state = %+v", snapshot)
	}

	timedOut := coordinator.ShutdownDetailed(context.Background(), campaign, "flare", Observations{TimedOut: true})
	if !timedOut.TimedOut || !timedOut.ConfirmedAt.IsZero() {
		t.Fatalf("timeout trace = %+v", timedOut)
	}
	if got := coordinator.State.Snapshot(); !got.TimedOut || got.Released || got.PressureObserved {
		t.Fatalf("state after timeout = %+v, want reset to timed-out, unobserved", got)
	}
	if err := TraceError(timedOut); err == nil || err.Error() != "relief valve timeout" {
		t.Fatalf("TraceError = %v", err)
	}
}
