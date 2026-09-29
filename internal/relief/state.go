package relief

import (
	"github.com/wyw14/solo-039/internal/model"
	"sync"
)

type State struct {
	mu    sync.RWMutex
	value model.Relief
}

func New() *State { return &State{value: model.Relief{Valve: "flare"}} }

func (s *State) Begin(valve string) {
	s.mu.Lock()
	s.value.Valve = valve
	s.value.Released = false
	s.value.TimedOut = false
	s.value.Pressure = 0
	s.value.PressureObserved = false
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
	}
	s.mu.Unlock()
}

// ObservePressure records one pressure reading. Readings only add information:
// a stale or out-of-order sample never erases the fact that pressure was observed.
func (s *State) ObservePressure(value float64) {
	s.mu.Lock()
	s.value.Pressure = value
	s.value.PressureObserved = true
	s.mu.Unlock()
}

func (s *State) Snapshot() model.Relief { s.mu.RLock(); defer s.mu.RUnlock(); return s.value }
