package relief

import (
	"context"
	"testing"

	"github.com/wyw14/solo-039/internal/model"
)

func newTestCampaign() model.Campaign { return model.NewCampaign("baseline") }

// 超时失败后重试成功：同一停机过程下必须保留两次原始尝试，
// 前次失败不能被重试成功覆盖。
func TestHistoryKeepsFailedAttemptAcrossRetry(t *testing.T) {
	coordinator := NewCoordinator()
	cpg := newTestCampaign()

	err, _ := coordinator.Shutdown(context.Background(), cpg, ShutdownOptions{
		Valve:        "flare",
		Pressures:    []float64{1.2, 1.18, 1.16},
		SafePressure: 0.2,
		Timeout:      true,
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}

	err, _ = coordinator.Shutdown(context.Background(), cpg, ShutdownOptions{
		Valve:        "flare",
		Pressures:    []float64{1.1, 0.6, 0.1},
		SafePressure: 0.2,
	})
	if err != nil {
		t.Fatalf("second attempt should succeed, got %v", err)
	}

	sessions := coordinator.History.Sessions()
	if len(sessions) != 1 {
		t.Fatalf("expected a single shutdown session, got %d", len(sessions))
	}
	session := sessions[0]
	if len(session.Attempts) != 2 {
		t.Fatalf("expected both attempts preserved, got %d", len(session.Attempts))
	}
	if session.Attempts[0].Sequence != 1 || session.Attempts[1].Sequence != 2 {
		t.Fatalf("attempt sequence broken: %d, %d", session.Attempts[0].Sequence, session.Attempts[1].Sequence)
	}

	failed := session.Attempts[0]
	if failed.Outcome != OutcomeValveTimeout {
		t.Fatalf("first attempt outcome = %s, want valve-timeout", failed.Outcome)
	}
	if !failed.TimedOut || failed.Error == "" {
		t.Fatalf("first attempt must retain timeout/error: %+v", failed)
	}
	if failed.PressureStatus != PressureHolds {
		t.Fatalf("timeout with high readings = %s, want holds (confirmed not depressurized)", failed.PressureStatus)
	}
	if failed.Command == nil || failed.Command.Action != "open" {
		t.Fatalf("first attempt must retain its original command: %+v", failed.Command)
	}

	succeeded := session.Attempts[1]
	if succeeded.Outcome != OutcomeDepressurized || succeeded.PressureStatus != PressureSafe {
		t.Fatalf("second attempt outcome = %s/%s, want depressurized/safe", succeeded.Outcome, succeeded.PressureStatus)
	}

	if session.Open {
		t.Fatal("session should report closed once depressurization confirmed")
	}
	if session.FinalOutcome != OutcomeDepressurized {
		t.Fatalf("final outcome = %s, want depressurized", session.FinalOutcome)
	}

	failures := coordinator.History.Failures()
	if len(failures) != 1 || failures[0].Sequence != 1 {
		t.Fatalf("retry must not erase the prior failure, got %+v", failures)
	}
	if coordinator.History.Count() != 2 {
		t.Fatalf("attempt count = %d, want 2", coordinator.History.Count())
	}
}

// 超时时没有任何压力观测：只能结论“尚未测到”，不能误报为已确认未降压。
func TestTimeoutWithoutReadingsIsUnmeasured(t *testing.T) {
	coordinator := NewCoordinator()
	err, _ := coordinator.Shutdown(context.Background(), newTestCampaign(), ShutdownOptions{
		Valve:        "flare",
		SafePressure: 0.2,
		Timeout:      true,
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	latest, ok := coordinator.History.Latest()
	if !ok {
		t.Fatal("expected latest attempt")
	}
	if latest.PressureStatus != PressureUnmeasured {
		t.Fatalf("pressure status = %s, want unmeasured", latest.PressureStatus)
	}
	if latest.Outcome != OutcomeValveTimeout {
		t.Fatalf("outcome = %s, want valve-timeout", latest.Outcome)
	}
	if len(latest.Observations) != 0 {
		t.Fatalf("expected no pressure observations, got %d", len(latest.Observations))
	}
}

// 阀已动作、压力在下降但还没到阈值：结论为 falling，区别于 holds。
func TestFallingPressureStaysDistinctFromHolds(t *testing.T) {
	coordinator := NewCoordinator()
	err, _ := coordinator.Shutdown(context.Background(), newTestCampaign(), ShutdownOptions{
		Valve:        "flare",
		Pressures:    []float64{1.2, 1.0, 0.8},
		SafePressure: 0.2,
	})
	if err == nil {
		t.Fatal("expected non-safe evaluation error while pressure is still high")
	}
	latest, _ := coordinator.History.Latest()
	if latest.PressureStatus != PressureFalling {
		t.Fatalf("pressure status = %s, want falling", latest.PressureStatus)
	}
	if latest.Outcome != OutcomeInconclusive {
		t.Fatalf("outcome = %s, want inconclusive while falling above threshold", latest.Outcome)
	}

	err, _ = coordinator.Shutdown(context.Background(), newTestCampaign(), ShutdownOptions{
		Valve:        "flare",
		Pressures:    []float64{1.2, 1.2, 1.19},
		SafePressure: 0.2,
	})
	if err == nil {
		t.Fatal("expected non-safe evaluation error")
	}
	latest, _ = coordinator.History.Latest()
	if latest.PressureStatus != PressureHolds {
		t.Fatalf("pressure status = %s, want holds", latest.PressureStatus)
	}
}

// 慢阀位响应可以从历史中直接筛出，并附带发令到到位的延迟。
func TestSlowValveResponseIsRecorded(t *testing.T) {
	coordinator := NewCoordinator()
	err, _ := coordinator.Shutdown(context.Background(), newTestCampaign(), ShutdownOptions{
		Valve:          "flare",
		Pressures:      []float64{0.1},
		SafePressure:   0.2,
		ValveLatencyMS: 3500,
	})
	if err != nil {
		t.Fatalf("slow valve that still opens and depressurizes should succeed, got %v", err)
	}
	slow := coordinator.History.SlowValveResponses()
	if len(slow) != 1 {
		t.Fatalf("slow valve attempts = %d, want 1", len(slow))
	}
	if slow[0].ValveResponse == nil || !slow[0].ValveResponse.Slow || slow[0].ValveResponse.LatencyMS != 3500 {
		t.Fatalf("slow valve observation missing detail: %+v", slow[0].ValveResponse)
	}
	if slow[0].Outcome != OutcomeDepressurized {
		t.Fatal("slow response is distinct from failure: attempt still depressurized")
	}
}

// 新 campaign 的停机开启新过程，不挂到上一次未完成的停机上。
func TestNewCampaignOpensNewSession(t *testing.T) {
	coordinator := NewCoordinator()
	err, _ := coordinator.Shutdown(context.Background(), newTestCampaign(), ShutdownOptions{Timeout: true, SafePressure: 0.2})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	err, _ = coordinator.Shutdown(context.Background(), newTestCampaign(), ShutdownOptions{Pressures: []float64{0.1}, SafePressure: 0.2})
	if err != nil {
		t.Fatalf("expected successful shutdown for new campaign, got %v", err)
	}
	if sessions := coordinator.History.Sessions(); len(sessions) != 2 {
		t.Fatalf("expected two shutdown sessions, got %d", len(sessions))
	}
}
