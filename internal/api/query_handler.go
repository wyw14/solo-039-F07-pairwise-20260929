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

// reliefHistory returns the replay-friendly review document: every shutdown
// process with each original attempt (command, valve confirmation, pressure
// readings, timeout and conclusion). A failed attempt stays listed even after
// a later retry succeeds.
func (s *Server) reliefHistory(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.plant.ReliefHistory.Report())
}
