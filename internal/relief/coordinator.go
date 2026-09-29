package relief

import (
	"context"
	"errors"
	"time"

	"github.com/wyw14/solo-039/internal/model"
)

type Coordinator struct {
	State   *State
	History *History
}

// ShutdownOptions 描述一次泄压尝试的输入：发令目标阀、过程中的压力观测、
// 安全压力阈值、模拟阀位响应延迟以及是否超时。
type ShutdownOptions struct {
	Valve          string
	Pressures      []float64
	SafePressure   float64
	ValveLatencyMS int64
	Timeout        bool
	// ForceNew 为 true 时总是开启新的停机过程；否则同一 campaign 下尚未
	// 确认泄压完成的过程会把本次调用记为一次重试。
	ForceNew bool
}

type ShutdownResult struct {
	ReliefError   error
	SteamError    error
	Depressurized bool
	SteamClosed   bool
}

func NewCoordinator() *Coordinator { return &Coordinator{State: New(), History: NewHistory()} }

// Shutdown 执行一次泄压尝试。同一停机过程中前次尝试失败后再次调用，会挂到
// 同一个 ShutdownSession 上作为重试，而不是覆盖前次记录。
func (c *Coordinator) Shutdown(ctx context.Context, cpg model.Campaign, opts ShutdownOptions) (error, *ShutdownSession) {
	if err := ctx.Err(); err != nil {
		return err, nil
	}
	if opts.Valve == "" {
		opts.Valve = "flare"
	}
	if opts.SafePressure <= 0 {
		opts.SafePressure = model.DefaultLimits().MinPressure
	}

	session, ok := (*ShutdownSession)(nil), false
	if !opts.ForceNew {
		session, ok = c.History.Active()
	}
	if !ok || session.CampaignID != cpg.ID.String() {
		session = c.History.OpenShutdown(cpg, opts.Valve, opts.SafePressure)
	} else {
		session.BeginAttempt()
	}

	c.State.Begin(opts.Valve)
	command := model.NewCommand(cpg, opts.Valve, "open", 0)
	c.State.CommandSentAt(time.Now())
	session.RecordCommand(command)

	if opts.Timeout {
		c.State.Timeout()
		// 窗口内取得的压力观测仍然记录：等到截止时刻仍高于阈值，
		// 才能从“尚未测到”推进为“确认未降压”。
		for _, pressure := range opts.Pressures {
			c.State.ObservePressure(pressure)
			session.RecordPressure(pressure)
		}
		session.RecordValve("unknown", opts.ValveLatencyMS, true, true)
		err := errors.New("relief valve timeout")
		session.Finish(true, err)
		return err, session
	}

	c.State.Release()
	latencyMS := opts.ValveLatencyMS
	if latencyMS == 0 {
		if latency, ok := c.State.ValveLatency(); ok {
			latencyMS = latency.Milliseconds()
		}
	}
	session.RecordValve("open", latencyMS, time.Duration(latencyMS)*time.Millisecond >= SlowValveLatency, false)
	if len(opts.Pressures) == 0 {
		opts.Pressures = []float64{0}
	}
	for _, pressure := range opts.Pressures {
		c.State.ObservePressure(pressure)
		session.RecordPressure(pressure)
	}
	err := Evaluate(c.State.Snapshot(), opts.SafePressure).Error()
	session.Finish(false, err)
	return err, session
}

func (c *Coordinator) Safe() bool { return c.State.Snapshot().Released && !c.State.Snapshot().TimedOut }

func CombineShutdown(reliefErr, steamErr error, reliefState model.Relief, steamClosed bool) ShutdownResult {
	return ShutdownResult{ReliefError: reliefErr, SteamError: steamErr, Depressurized: reliefErr == nil && reliefState.Released && !reliefState.TimedOut, SteamClosed: steamErr == nil && steamClosed}
}

func (r ShutdownResult) Error() error {
	if r.ReliefError != nil {
		return r.ReliefError
	}
	return r.SteamError
}

func (r ShutdownResult) Safe() bool { return r.Depressurized && r.SteamClosed && r.Error() == nil }
