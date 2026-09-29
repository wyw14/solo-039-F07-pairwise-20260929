package relief

import (
	"context"
	"errors"
	"github.com/wyw14/solo-039/internal/model"
	"time"
)

type Coordinator struct{ State *State }

type ShutdownResult struct {
	ReliefError   error
	SteamError    error
	Depressurized bool
	SteamClosed   bool
}

func NewCoordinator() *Coordinator { return &Coordinator{State: New()} }

func (c *Coordinator) Shutdown(ctx context.Context, cpg model.Campaign, valve string, timeout bool) error {
	return TraceError(c.ShutdownDetailed(ctx, cpg, valve, Observations{TimedOut: timeout}))
}

// ShutdownDetailed runs one relief attempt and returns the full trace that
// links the command, the valve position confirmation, every pressure reading,
// the timeout flag and the attempt's final conclusion.
func (c *Coordinator) ShutdownDetailed(ctx context.Context, cpg model.Campaign, valve string, obs Observations) Trace {
	commandAt := time.Now()
	trace := Trace{
		Campaign:     cpg,
		Valve:        valve,
		CommandAt:    commandAt,
		SafePressure: DefaultSafePressure,
		EndedAt:      commandAt,
	}
	for _, sample := range obs.Pressures {
		reading := PressureReading{At: sample.At, Pressure: sample.Value}
		if reading.At.IsZero() {
			reading.At = commandAt
		}
		trace.Readings = append(trace.Readings, reading)
	}

	c.State.Begin(valve)
	if deadline, ok := ctx.Deadline(); ok {
		trace.DeadlineAt = deadline
	}

	if obs.TimedOut {
		c.State.Timeout()
		trace.TimedOut = true
		trace.EndedAt = time.Now()
		if !trace.DeadlineAt.IsZero() && trace.EndedAt.Before(trace.DeadlineAt) {
			trace.EndedAt = trace.DeadlineAt
		}
		return trace
	}

	if obs.Response > 0 {
		trace.ConfirmedAt = commandAt.Add(obs.Response)
	} else {
		trace.ConfirmedAt = time.Now()
	}
	c.State.Release()
	for _, reading := range trace.Readings {
		c.State.ObservePressure(reading.Pressure)
	}
	trace.EndedAt = time.Now()
	return trace
}

func (c *Coordinator) Safe() bool { return c.State.Snapshot().Released && !c.State.Snapshot().TimedOut }

func CombineShutdown(reliefErr, steamErr error, reliefState model.Relief, safePressure float64, steamClosed bool) ShutdownResult {
	depressurized := reliefErr == nil &&
		reliefState.Released &&
		!reliefState.TimedOut &&
		reliefState.PressureObserved &&
		reliefState.Pressure <= safePressure
	return ShutdownResult{ReliefError: reliefErr, SteamError: steamErr, Depressurized: depressurized, SteamClosed: steamErr == nil && steamClosed}
}

func (r ShutdownResult) Error() error {
	if r.ReliefError != nil {
		return r.ReliefError
	}
	return r.SteamError
}

func (r ShutdownResult) Safe() bool { return r.Depressurized && r.SteamClosed && r.Error() == nil }

// TraceError derives the command-level error from a recorded attempt.
func TraceError(trace Trace) error {
	if trace.TimedOut {
		return errors.New("relief valve timeout")
	}
	return nil
}
