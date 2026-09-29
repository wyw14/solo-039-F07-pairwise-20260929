package relief

import (
	"sync"
	"time"

	"github.com/wyw14/solo-039/internal/model"
)

// SlowValveLatency 是阀门从发令到确认到位的慢响应门槛。
const SlowValveLatency = 2 * time.Second

type State struct {
	mu          sync.RWMutex
	value       model.Relief
	commandAt   time.Time
	respondedAt time.Time
}

func New() *State { return &State{value: model.Relief{Valve: "flare"}} }

func (s *State) Begin(valve string) {
	s.mu.Lock()
	s.value.Valve = valve
	s.value.Released = false
	s.value.TimedOut = false
	s.commandAt = time.Time{}
	s.respondedAt = time.Time{}
	s.mu.Unlock()
}

// CommandSentAt 记录泄压阀发令时刻，供判定阀位响应延迟。
func (s *State) CommandSentAt(at time.Time) {
	s.mu.Lock()
	s.commandAt = at
	s.mu.Unlock()
}

func (s *State) Timeout() {
	s.mu.Lock()
	s.value.TimedOut = true
	s.value.Released = false
	s.mu.Unlock()
}

func (s *State) Release() {
	s.mu.Lock()
	if !s.value.TimedOut {
		s.value.Released = true
		if s.respondedAt.IsZero() {
			s.respondedAt = time.Now()
		}
	}
	s.mu.Unlock()
}

// ObservePressure 记录一拍压力观测。
func (s *State) ObservePressure(pressure float64) {
	s.mu.Lock()
	s.value.Pressure = pressure
	s.mu.Unlock()
}

// ValveLatency 返回发令到阀位确认的延迟；缺少任一端时 ok 为 false。
func (s *State) ValveLatency() (time.Duration, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.commandAt.IsZero() || s.respondedAt.IsZero() {
		return 0, false
	}
	return s.respondedAt.Sub(s.commandAt), true
}

func (s *State) Snapshot() model.Relief {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.value
}
