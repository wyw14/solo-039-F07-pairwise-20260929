package relief

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/wyw14/solo-039/internal/model"
)

// PressureStatus 区分“尚未测到压力”与“已观测但确认未降压”。
type PressureStatus string

const (
	PressureUnmeasured PressureStatus = "unmeasured" // 该次尝试尚未取得任何压力观测
	PressureFalling    PressureStatus = "falling"    // 压力持续下降，但还未达到安全阈值
	PressureSafe       PressureStatus = "safe"       // 压力已降到安全阈值以下
	PressureHolds      PressureStatus = "holds"      // 有观测且阀门已开/已超时，压力仍维持在阈值以上：确认未降压
)

// Outcome 是单次泄压尝试的最终结论。
type Outcome string

const (
	OutcomeDepressurized Outcome = "depressurized"  // 确认已降压
	OutcomeValveTimeout  Outcome = "valve-timeout"  // 阀门未在窗口内动作
	OutcomePressureHolds Outcome = "pressure-holds" // 阀已动作但压力确认未下降
	OutcomeInconclusive  Outcome = "inconclusive"   // 发令成功但证据不足以下结论
)

// PressureObservation 是一次压力观测；Delta 为相对上一观测的变化，Falling 表示该拍在下降。
type PressureObservation struct {
	At       time.Time `json:"at"`
	Pressure float64   `json:"pressure"`
	Safe     bool      `json:"safe"`
	Delta    float64   `json:"delta"`
	Falling  bool      `json:"falling"`
}

// ValveObservation 记录阀位响应：发令后多久到位、是否慢响应、是否始终未动作。
type ValveObservation struct {
	At        time.Time `json:"at"`
	Position  string    `json:"position"` // open / closed / unknown
	LatencyMS int64     `json:"latency_ms"`
	Slow      bool      `json:"slow"`
	TimedOut  bool      `json:"timed_out"`
	Observed  bool      `json:"observed"`
}

// Attempt 是一次停机过程中的一次原始泄压尝试。重试只会追加新的 Attempt，
// 永远不会覆盖前一次的发令、观测、超时与结论。
type Attempt struct {
	Sequence       int                   `json:"sequence"`
	Valve          string                `json:"valve"`
	StartedAt      time.Time             `json:"started_at"`
	FinishedAt     *time.Time            `json:"finished_at,omitempty"`
	Command        *model.Command        `json:"command,omitempty"`
	CommandSentAt  time.Time             `json:"command_sent_at,omitempty"`
	ValveResponse  *ValveObservation     `json:"valve_response,omitempty"`
	Observations   []PressureObservation `json:"observations"`
	TimedOut       bool                  `json:"timed_out"`
	PressureStatus PressureStatus        `json:"pressure_status"`
	Outcome        Outcome               `json:"outcome"`
	Conclusion     string                `json:"conclusion"`
	Error          string                `json:"error,omitempty"`
}

// SessionReport 是一次停机过程的可序列化视图，按时间串起其全部尝试。
type SessionReport struct {
	ID              string     `json:"id"`
	CampaignID      string     `json:"campaign_id"`
	Valve           string     `json:"valve"`
	SafePressure    float64    `json:"safe_pressure"`
	StartedAt       time.Time  `json:"started_at"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
	Open            bool       `json:"open"`
	Attempts        []Attempt  `json:"attempts"`
	FinalOutcome    Outcome    `json:"final_outcome,omitempty"`
	FinalConclusion string     `json:"final_conclusion,omitempty"`
}

// ShutdownSession 对应同一次停机：第一次泄压失败后，重试仍挂在这个 session 上。
type ShutdownSession struct {
	h *History

	id           string
	CampaignID   string
	Valve        string
	SafePressure float64
	startedAt    time.Time
	finishedAt   time.Time
	seq          int
	attempts     []*Attempt
	finalOutcome Outcome
}

type History struct {
	mu       sync.Mutex
	sessions []*ShutdownSession
}

func NewHistory() *History { return &History{sessions: make([]*ShutdownSession, 0, 8)} }

// OpenShutdown 开始一次新的停机过程，并立即登记第一次泄压尝试。
func (h *History) OpenShutdown(c model.Campaign, valve string, safePressure float64) *ShutdownSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := &ShutdownSession{
		h:            h,
		id:           fmt.Sprintf("%s#%d", c.ID.String(), len(h.sessions)+1),
		CampaignID:   c.ID.String(),
		Valve:        valve,
		SafePressure: safePressure,
		startedAt:    time.Now(),
	}
	s.beginLocked()
	h.sessions = append(h.sessions, s)
	return s
}

// BeginAttempt 在同一停机过程中登记一次重试。
func (s *ShutdownSession) BeginAttempt() {
	s.h.mu.Lock()
	defer s.h.mu.Unlock()
	s.beginLocked()
}

func (s *ShutdownSession) beginLocked() {
	s.seq++
	s.attempts = append(s.attempts, &Attempt{
		Sequence:       s.seq,
		Valve:          s.Valve,
		StartedAt:      time.Now(),
		Observations:   make([]PressureObservation, 0, 4),
		PressureStatus: PressureUnmeasured,
		Outcome:        OutcomeInconclusive,
	})
}

func (s *ShutdownSession) currentLocked() *Attempt { return s.attempts[len(s.attempts)-1] }

// RecordCommand 记录向泄压阀发出的命令（发令）。
func (s *ShutdownSession) RecordCommand(command model.Command) {
	s.h.mu.Lock()
	defer s.h.mu.Unlock()
	a := s.currentLocked()
	a.Command = &command
	a.CommandSentAt = time.Now()
}

// RecordValve 记录阀位观测：latencyMS 为自发令到阀位确认的延迟，
// slow 标记慢响应，timedOut 表示窗口内阀门始终未动作。
func (s *ShutdownSession) RecordValve(position string, latencyMS int64, slow, timedOut bool) {
	s.h.mu.Lock()
	defer s.h.mu.Unlock()
	a := s.currentLocked()
	a.ValveResponse = &ValveObservation{
		At:        time.Now(),
		Position:  position,
		LatencyMS: latencyMS,
		Slow:      slow,
		TimedOut:  timedOut,
		Observed:  true,
	}
	a.TimedOut = a.TimedOut || timedOut
}

// RecordPressure 追加一次压力观测。
func (s *ShutdownSession) RecordPressure(pressure float64) PressureObservation {
	s.h.mu.Lock()
	defer s.h.mu.Unlock()
	a := s.currentLocked()
	delta := 0.0
	if n := len(a.Observations); n > 0 {
		delta = pressure - a.Observations[n-1].Pressure
	}
	obs := PressureObservation{
		At:       time.Now(),
		Pressure: pressure,
		Safe:     pressure <= s.SafePressure,
		Delta:    delta,
		Falling:  delta < 0,
	}
	a.Observations = append(a.Observations, obs)
	return obs
}

// Finish 给当前尝试下最终结论；err 为该次尝试返回的错误（可为 nil）。
// 返回该次尝试的快照。
func (s *ShutdownSession) Finish(timedOut bool, err error) Attempt {
	s.h.mu.Lock()
	defer s.h.mu.Unlock()
	a := s.currentLocked()
	finishedAt := time.Now()
	a.FinishedAt = &finishedAt
	a.TimedOut = a.TimedOut || timedOut
	if err != nil {
		a.Error = err.Error()
	}
	a.PressureStatus = classifyPressure(a)
	a.Outcome = classifyOutcome(a)
	a.Conclusion = conclusionFor(a)
	s.finalOutcome = a.Outcome
	if a.Outcome == OutcomeDepressurized {
		s.finishedAt = *a.FinishedAt
	}
	return cloneAttempt(a)
}

func classifyPressure(a *Attempt) PressureStatus {
	if len(a.Observations) == 0 {
		return PressureUnmeasured
	}
	last := a.Observations[len(a.Observations)-1]
	if last.Safe {
		return PressureSafe
	}
	if a.TimedOut {
		// 等到窗口结束仍测得高于阈值的压力：确认未降压。
		return PressureHolds
	}
	if len(a.Observations) >= 2 {
		allFalling := true
		for _, obs := range a.Observations[1:] {
			if obs.Delta >= 0 {
				allFalling = false
				break
			}
		}
		if allFalling {
			return PressureFalling
		}
	}
	return PressureHolds
}

func classifyOutcome(a *Attempt) Outcome {
	switch {
	case a.TimedOut:
		return OutcomeValveTimeout
	case a.PressureStatus == PressureSafe:
		return OutcomeDepressurized
	case a.PressureStatus == PressureHolds:
		return OutcomePressureHolds
	default:
		return OutcomeInconclusive
	}
}

func conclusionFor(a *Attempt) string {
	switch a.Outcome {
	case OutcomeDepressurized:
		return "depressurization confirmed: pressure observed below safe threshold"
	case OutcomeValveTimeout:
		if a.PressureStatus == PressureUnmeasured {
			return "relief valve did not respond within the window and no pressure reading was obtained"
		}
		return "relief valve timed out and pressure was confirmed holding above safe threshold"
	case OutcomePressureHolds:
		return "relief valve responded but pressure remains above safe threshold: depressurization not confirmed"
	default:
		if a.PressureStatus == PressureUnmeasured {
			return "relief command issued but no pressure observation was recorded yet"
		}
		return "pressure is falling across observations but has not reached the safe threshold"
	}
}

func cloneAttempt(a *Attempt) Attempt {
	copyValue := *a
	if a.Command != nil {
		command := *a.Command
		copyValue.Command = &command
	}
	if a.ValveResponse != nil {
		valve := *a.ValveResponse
		copyValue.ValveResponse = &valve
	}
	copyValue.Observations = append([]PressureObservation(nil), a.Observations...)
	return copyValue
}

// Report 返回该停机过程的快照（含其全部原始尝试）。
func (s *ShutdownSession) Report() SessionReport {
	s.h.mu.Lock()
	defer s.h.mu.Unlock()
	return s.reportLocked()
}

func (s *ShutdownSession) reportLocked() SessionReport {
	attempts := make([]Attempt, 0, len(s.attempts))
	for _, a := range s.attempts {
		attempts = append(attempts, cloneAttempt(a))
	}
	var finishedAt *time.Time
	if !s.finishedAt.IsZero() {
		finishedAt = &s.finishedAt
	}
	open := s.finalOutcome != OutcomeDepressurized
	return SessionReport{
		ID:              s.id,
		CampaignID:      s.CampaignID,
		Valve:           s.Valve,
		SafePressure:    s.SafePressure,
		StartedAt:       s.startedAt,
		FinishedAt:      finishedAt,
		Open:            open,
		Attempts:        attempts,
		FinalOutcome:    s.finalOutcome,
		FinalConclusion: sessionConclusionLocked(s),
	}
}

func sessionConclusionLocked(s *ShutdownSession) string {
	if s.finalOutcome == OutcomeDepressurized {
		return fmt.Sprintf("depressurization confirmed after %d attempt(s)", len(s.attempts))
	}
	if len(s.attempts) == 0 {
		return ""
	}
	return s.currentLocked().Conclusion
}

// Sessions 返回全部停机过程（含每次原始尝试）的快照。
func (h *History) Sessions() []SessionReport {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]SessionReport, 0, len(h.sessions))
	for _, s := range h.sessions {
		out = append(out, s.reportLocked())
	}
	return out
}

// Attempts 返回跨停机过程的全部原始尝试，按发生顺序排列。
func (h *History) Attempts() []Attempt {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Attempt, 0)
	for _, s := range h.sessions {
		for _, a := range s.attempts {
			out = append(out, cloneAttempt(a))
		}
	}
	return out
}

// Latest 返回最近一次尝试（无论成功或失败）。
func (h *History) Latest() (Attempt, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.sessions) == 0 {
		return Attempt{}, false
	}
	last := h.sessions[len(h.sessions)-1]
	if len(last.attempts) == 0 {
		return Attempt{}, false
	}
	return cloneAttempt(last.currentLocked()), true
}

// Failures 返回每一次失败的原始尝试：超时、命令出错或确认压力未降。
// 重试成功不会移除或覆盖这里的前次失败。
func (h *History) Failures() []Attempt {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Attempt, 0)
	for _, s := range h.sessions {
		for _, a := range s.attempts {
			if a.Error != "" || a.TimedOut || a.Outcome == OutcomePressureHolds {
				out = append(out, cloneAttempt(a))
			}
		}
	}
	return out
}

// SlowValveResponses 返回阀门响应被标记为慢的每一次尝试。
func (h *History) SlowValveResponses() []Attempt {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Attempt, 0)
	for _, s := range h.sessions {
		for _, a := range s.attempts {
			if a.ValveResponse != nil && a.ValveResponse.Slow {
				out = append(out, cloneAttempt(a))
			}
		}
	}
	return out
}

// Count 返回累计泄压尝试次数（含重试）。
func (h *History) Count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	total := 0
	for _, s := range h.sessions {
		total += len(s.attempts)
	}
	return total
}

// ShutdownCount 返回停机过程次数。
func (h *History) ShutdownCount() int { h.mu.Lock(); defer h.mu.Unlock(); return len(h.sessions) }

// CountPressureStatus 统计各压力结论的尝试数，例如区分未测到与确认未降压。
func (h *History) CountPressureStatus() map[PressureStatus]int {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[PressureStatus]int{}
	for _, s := range h.sessions {
		for _, a := range s.attempts {
			out[a.PressureStatus]++
		}
	}
	return out
}

// Active 返回最近一个尚未确认泄压完成的停机过程（可能正在等待重试）。
func (h *History) Active() (*ShutdownSession, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.sessions) == 0 {
		return nil, false
	}
	last := h.sessions[len(h.sessions)-1]
	if last.finalOutcome == OutcomeDepressurized {
		return nil, false
	}
	return last, true
}

var errAttemptMissing = errors.New("relief attempt has no recorded command")

// CommandKey 暴露该次尝试发令的命令键，便于复盘报告回溯到具体命令代号。
func (a Attempt) CommandKey() (string, error) {
	if a.Command == nil {
		return "", errAttemptMissing
	}
	return a.Command.Key(), nil
}
