package api

import (
	"github.com/wyw14/solo-039/internal/journal"
	"net/http"
)

func (s *Server) snapshot(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.plant.Snapshot())
}

func (s *Server) evidence(w http.ResponseWriter, r *http.Request) {
	revision := s.plant.Campaign.Snapshot().Revision.String()
	writeJSON(w, http.StatusOK, map[string]any{"revision": revision, "proofs": s.plant.Evidence.Independent(revision)})
}

func (s *Server) recovery(w http.ResponseWriter, r *http.Request) {
	plan := journal.DefaultRecoveryPlan()
	writeJSON(w, http.StatusOK, map[string]any{"pending": plan.Pending(), "complete": plan.Complete()})
}

func (s *Server) profile(w http.ResponseWriter, r *http.Request) {
	gradient, err := s.plant.Profile.Gradient()
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"readings": s.plant.Profile.Ordered(), "gradient": gradient, "stable": s.plant.Profile.Stable(40)})
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.plant.Metrics.Snapshot())
}

// reliefHistory 返回按停机过程串起的完整泄压历史：发令、阀位响应、
// 压力观测序列、超时与每次尝试的最终结论。
func (s *Server) reliefHistory(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"state":         s.plant.Relief.State.Snapshot(),
		"shutdowns":     s.plant.ReliefHistory.ShutdownCount(),
		"attempt_total": s.plant.ReliefHistory.Count(),
		"sessions":      s.plant.ReliefHistory.Sessions(),
		"attempts":      s.plant.ReliefHistory.Attempts(),
		"failures":      s.plant.ReliefHistory.Failures(),
		"slow_valves":   s.plant.ReliefHistory.SlowValveResponses(),
		"pressure":      s.plant.ReliefHistory.CountPressureStatus(),
	})
}
