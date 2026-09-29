package relief

import (
	"errors"
	"github.com/google/uuid"
	"github.com/wyw14/solo-039/internal/model"
	"sync"
	"time"
)

// DefaultSafePressure is the pressure (in operating units) at or below which
// the vessel is considered depressurized.
const DefaultSafePressure = 0.1

// DefaultSlowResponse is how long a valve may take to confirm its position
// before the attempt is flagged as slow (but not necessarily timed out).
const DefaultSlowResponse = 15 * time.Second

// Outcome is the per-attempt final conclusion.
type Outcome string

const (
	OutcomeDepressurized Outcome = "depressurized"  // valve confirmed and pressure measured at/under safe threshold
	OutcomeNotMeasured   Outcome = "not-measured"   // valve confirmed, but no pressure reading ever came back
	OutcomeHeldPressure  Outcome = "pressure-held"  // pressure was measured and stayed above the safe threshold
	OutcomeTimedOut      Outcome = "timed-out"      // valve never confirmed before the deadline
	OutcomeCommandFailed Outcome = "command-failed" // command was rejected before the valve could act
	OutcomeAbandoned     Outcome = "abandoned"      // shutdown process ended without a final conclusion
)

// PressureReading is one raw pressure observation gathered during an attempt.
type PressureReading struct {
	At       time.Time `json:"at"`
	Pressure float64   `json:"pressure"`
}

// Attempt is one raw relief try. Attempts are append-only: issuing a retry
// appends a new Attempt and never mutates or hides an earlier failure.
type Attempt struct {
	Index        int       `json:"index"` // 1-based within the shutdown session
	Valve        string    `json:"valve"`
	CommandAt    time.Time `json:"command_at"`
	DeadlineAt   time.Time `json:"deadline_at"`
	ConfirmedAt  time.Time `json:"confirmed_at,omitempty"`
	TimedOut     bool      `json:"timed_out"`
	ResponseMS   int64     `json:"response_ms,omitempty"`
	ResponseSlow bool      `json:"response_slow"`

	CommandError string `json:"command_error,omitempty"`

	Readings          []PressureReading `json:"readings"`
	PressureObserved  bool              `json:"pressure_observed"`
	FinalPressure     float64           `json:"final_pressure,omitempty"`
	PressureDelta     float64           `json:"pressure_delta"`
	PressureDeclining bool              `json:"pressure_declining"`
	SafePressure      float64           `json:"safe_pressure"`

	Outcome    Outcome   `json:"outcome"`
	Conclusion string    `json:"conclusion"`
	At         time.Time `json:"at"`
}

// Failed reports whether this attempt failed to establish depressurization.
// Note that "not-measured" is not a failure: absence of evidence is recorded
// separately from confirmed high pressure.
func (a Attempt) Failed() bool {
	switch a.Outcome {
	case OutcomeTimedOut, OutcomeHeldPressure, OutcomeCommandFailed:
		return true
	default:
		return false
	}
}

// Error describes why the attempt is not a confirmed depressurization.
// A not-measured attempt is not an error: it is an unverified one.
func (a Attempt) Error() error {
	if !a.Failed() {
		return nil
	}
	return errors.New(a.Conclusion)
}

// Session groups every attempt belonging to one shutdown process. A shutdown
// process stays open across retries until one attempt succeeds or the process
// is abandoned.
type Session struct {
	CampaignID   uuid.UUID `json:"campaign_id"`
	Valve        string    `json:"valve"`
	SafePressure float64   `json:"safe_pressure"`
	StartedAt    time.Time `json:"started_at"`
	EndedAt      time.Time `json:"ended_at,omitempty"`
	Resolved     bool      `json:"resolved"`
	Succeeded    bool      `json:"succeeded"`
	FinalOutcome Outcome   `json:"final_outcome"`
	Conclusion   string    `json:"conclusion"`
	Attempts     []Attempt `json:"attempts"`
}

// PressureSample is an observed pressure value offered to the coordinator.
type PressureSample struct {
	At    time.Time
	Value float64
}

// Observations are the field observations attached to one shutdown call:
// how long the valve took to confirm, whether it missed the deadline, and the
// pressure samples collected meanwhile.
type Observations struct {
	TimedOut  bool
	Response  time.Duration
	Pressures []PressureSample
}

// Trace is the full record of one relief attempt, handed to the history by the
// coordinator. Timestamps are captured by the coordinator so the history stays
// a pure ledger.
type Trace struct {
	Campaign     model.Campaign
	Valve        string
	CommandAt    time.Time
	DeadlineAt   time.Time
	ConfirmedAt  time.Time
	TimedOut     bool
	CommandError string
	Readings     []PressureReading
	SafePressure float64
	EndedAt      time.Time
}

type History struct {
	mu            sync.Mutex
	sessions      []*Session
	active        *Session
	slowThreshold time.Duration
}

func NewHistory() *History { return NewHistoryWithSlowResponse(DefaultSlowResponse) }

func NewHistoryWithSlowResponse(threshold time.Duration) *History {
	return &History{sessions: make([]*Session, 0, 8), slowThreshold: threshold}
}

// BeginSession opens (or reopens) the shutdown process for a campaign. A retry
// against the same still-open campaign keeps appending to the same session.
func (h *History) BeginSession(campaign model.Campaign, valve string, safePressure float64) *Session {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active != nil && h.active.CampaignID == campaign.ID {
		h.active.Valve = valve
		h.active.SafePressure = safePressure
		return h.active
	}
	if h.active != nil {
		h.abandonLocked(h.active)
	}
	session := &Session{
		CampaignID:   campaign.ID,
		Valve:        valve,
		SafePressure: safePressure,
		StartedAt:    time.Now(),
		Attempts:     make([]Attempt, 0, 4),
	}
	h.sessions = append(h.sessions, session)
	h.active = session
	return session
}

// Record appends one completed attempt to the active shutdown process (opening
// a session if BeginSession was not called). A successful attempt resolves the
// process; any other outcome leaves it open for a retry.
func (h *History) Record(trace Trace) Attempt {
	h.mu.Lock()
	defer h.mu.Unlock()
	session := h.active
	if session == nil || session.CampaignID != trace.Campaign.ID {
		if h.active != nil {
			h.abandonLocked(h.active)
		}
		session = &Session{
			CampaignID:   trace.Campaign.ID,
			Valve:        trace.Valve,
			SafePressure: trace.SafePressure,
			StartedAt:    trace.CommandAt,
			Attempts:     make([]Attempt, 0, 4),
		}
		h.sessions = append(h.sessions, session)
		h.active = session
	}

	attempt := Attempt{
		Index:        len(session.Attempts) + 1,
		Valve:        trace.Valve,
		CommandAt:    trace.CommandAt,
		DeadlineAt:   trace.DeadlineAt,
		ConfirmedAt:  trace.ConfirmedAt,
		TimedOut:     trace.TimedOut,
		CommandError: trace.CommandError,
		Readings:     append(make([]PressureReading, 0, len(trace.Readings)), trace.Readings...),
		SafePressure: trace.SafePressure,
		At:           trace.EndedAt,
	}
	if attempt.At.IsZero() {
		attempt.At = time.Now()
	}
	if !trace.ConfirmedAt.IsZero() {
		response := trace.ConfirmedAt.Sub(trace.CommandAt)
		if response > 0 {
			attempt.ResponseMS = response.Milliseconds()
			attempt.ResponseSlow = response > h.slowThreshold
		}
	}
	h.summarizePressure(&attempt)
	h.conclude(&attempt)

	session.Attempts = append(session.Attempts, attempt)
	if attempt.Outcome == OutcomeDepressurized {
		session.Resolved = true
		session.Succeeded = true
		session.FinalOutcome = attempt.Outcome
		session.Conclusion = attempt.Conclusion
		session.EndedAt = attempt.At
		h.active = nil
	}
	return attempt
}

func (h *History) summarizePressure(a *Attempt) {
	if len(a.Readings) == 0 {
		return
	}
	a.PressureObserved = true
	a.FinalPressure = a.Readings[len(a.Readings)-1].Pressure
	a.PressureDelta = a.FinalPressure - a.Readings[0].Pressure
	declining := len(a.Readings) > 1
	for i := 1; i < len(a.Readings); i++ {
		if a.Readings[i].Pressure >= a.Readings[i-1].Pressure {
			declining = false
			break
		}
	}
	a.PressureDeclining = declining
}

func (h *History) conclude(a *Attempt) {
	switch {
	case a.TimedOut:
		a.Outcome = OutcomeTimedOut
		a.Conclusion = "relief valve did not confirm before deadline"
	case a.ConfirmedAt.IsZero():
		a.Outcome = OutcomeTimedOut
		a.Conclusion = "relief valve position was never confirmed"
	case a.CommandError != "":
		a.Outcome = OutcomeCommandFailed
		a.Conclusion = "relief command rejected: " + a.CommandError
	case !a.PressureObserved:
		a.Outcome = OutcomeNotMeasured
		a.Conclusion = "valve confirmed open but pressure was never measured"
	case a.FinalPressure > a.SafePressure:
		a.Outcome = OutcomeHeldPressure
		if a.PressureDeclining {
			a.Conclusion = "pressure still falling but remains above safe threshold"
		} else {
			a.Conclusion = "pressure confirmed above safe threshold with no sustained decline"
		}
	default:
		a.Outcome = OutcomeDepressurized
		a.Conclusion = "depressurization confirmed at or below safe pressure"
	}
}

func (h *History) abandonLocked(session *Session) {
	last := OutcomeAbandoned
	conclusion := "shutdown process abandoned before a final conclusion"
	if n := len(session.Attempts); n > 0 {
		last = session.Attempts[n-1].Outcome
	}
	session.Resolved = true
	session.Succeeded = false
	session.FinalOutcome = last
	session.Conclusion = conclusion
	session.EndedAt = time.Now()
	if h.active == session {
		h.active = nil
	}
}

// Latest returns the most recent attempt across every shutdown process.
func (h *History) Latest() (Attempt, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := len(h.sessions) - 1; i >= 0; i-- {
		if attempts := h.sessions[i].Attempts; len(attempts) > 0 {
			return attempts[len(attempts)-1], true
		}
	}
	return Attempt{}, false
}

// Failures returns every failed attempt, in chronological order. Retries do
// not replace earlier entries, so each failed try remains reviewable.
func (h *History) Failures() []Attempt {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Attempt, 0)
	for _, session := range h.sessions {
		for _, attempt := range session.Attempts {
			if attempt.Failed() {
				out = append(out, attempt)
			}
		}
	}
	return out
}

// Attempts returns every raw attempt ever recorded, in chronological order.
func (h *History) Attempts() []Attempt {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Attempt, 0)
	for _, session := range h.sessions {
		out = append(out, session.Attempts...)
	}
	return out
}

func cloneSession(s *Session) Session {
	clone := *s
	clone.Attempts = append(make([]Attempt, 0, len(s.Attempts)), s.Attempts...)
	return clone
}

// Sessions returns all shutdown processes, each with every original attempt.
func (h *History) Sessions() []Session {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Session, 0, len(h.sessions))
	for _, session := range h.sessions {
		out = append(out, cloneSession(session))
	}
	return out
}

// SessionForCampaign returns the shutdown process for a campaign, if any.
func (h *History) SessionForCampaign(id uuid.UUID) (Session, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, session := range h.sessions {
		if session.CampaignID == id {
			return cloneSession(session), true
		}
	}
	return Session{}, false
}

// Report is the replay-friendly review document: one entry per shutdown
// process, carrying every original attempt so a retry cannot mask an earlier
// failure.
type Report struct {
	TotalAttempts  int       `json:"total_attempts"`
	FailedAttempts int       `json:"failed_attempts"`
	OpenProcesses  int       `json:"open_processes"`
	Sessions       []Session `json:"sessions"`
}

func (h *History) Report() Report {
	h.mu.Lock()
	defer h.mu.Unlock()
	report := Report{Sessions: make([]Session, 0, len(h.sessions))}
	for _, session := range h.sessions {
		for _, attempt := range session.Attempts {
			report.TotalAttempts++
			if attempt.Failed() {
				report.FailedAttempts++
			}
		}
		if !session.Resolved {
			report.OpenProcesses++
		}
		report.Sessions = append(report.Sessions, cloneSession(session))
	}
	return report
}

// Count returns the total number of recorded attempts.
func (h *History) Count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	total := 0
	for _, session := range h.sessions {
		total += len(session.Attempts)
	}
	return total
}
