package relief

import (
	"errors"
	"github.com/wyw14/solo-039/internal/model"
)

type Evaluation struct {
	PressureSafe     bool
	PressureObserved bool
	ValveReleased    bool
	TimedOut         bool
	Message          string
}

func Evaluate(state model.Relief, safePressure float64) Evaluation {
	value := Evaluation{
		PressureSafe:     state.PressureObserved && state.Pressure <= safePressure,
		PressureObserved: state.PressureObserved,
		ValveReleased:    state.Released,
		TimedOut:         state.TimedOut,
	}
	switch {
	case value.TimedOut:
		value.Message = "relief valve timed out"
	case !value.ValveReleased:
		value.Message = "relief path not confirmed"
	case !value.PressureObserved:
		// The valve moved, but no pressure reading ever came back: the system
		// is unverified, which is different from a verified failure to drop.
		value.Message = "depressurization not yet measured"
	case !value.PressureSafe:
		value.Message = "pressure confirmed above safe threshold"
	default:
		value.Message = "depressurization complete"
	}
	return value
}

func (e Evaluation) Error() error {
	if e.PressureObserved && e.PressureSafe && e.ValveReleased && !e.TimedOut {
		return nil
	}
	return errors.New(e.Message)
}

func (e Evaluation) Safe() bool { return e.Error() == nil }
