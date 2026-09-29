package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/wyw14/solo-039/internal/relief"
)

func (s *Server) transition(w http.ResponseWriter, r *http.Request) {
	var input transitionRequest
	if err := decodeJSON(r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	value, err := s.plant.Transition(r.Context(), input.Grade, input.Source)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, value)
}

func (s *Server) sample(w http.ResponseWriter, r *http.Request) {
	var input sampleRequest
	if err := decodeJSON(r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if input.At == 0 {
		input.At = time.Now().Unix()
	}
	count, err := s.plant.AddSample(input.Value, input.At)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "count": count})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"count": count})
}

func (s *Server) calibrate(w http.ResponseWriter, r *http.Request) {
	revision := s.plant.CalibrateAnalyzer()
	writeJSON(w, http.StatusAccepted, map[string]any{"calibration_revision": revision})
}

func (s *Server) routeProduct(w http.ResponseWriter, r *http.Request) {
	var input routeRequest
	if err := decodeJSON(r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.plant.RouteProduct(input.Target); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"target": input.Target})
}

func (s *Server) lease(w http.ResponseWriter, r *http.Request) {
	var input leaseRequest
	if err := decodeJSON(r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	value, err := s.plant.AcquireReflux(input.Owner, input.Amount)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, value)
}

func (s *Server) shutdown(w http.ResponseWriter, r *http.Request) {
	var input shutdownRequest
	// The body is optional; keep accepting the legacy ?relief_timeout query.
	if data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	} else if len(data) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	if timeout, err := boolQuery(r, "relief_timeout"); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	} else if timeout {
		input.TimedOut = true
	}

	obs := relief.Observations{TimedOut: input.TimedOut, Response: time.Duration(input.ValveResponseMS) * time.Millisecond}
	for _, sample := range input.Pressures {
		value := relief.PressureSample{Value: sample.Pressure}
		if sample.At != 0 {
			value.At = time.Unix(sample.At, 0)
		}
		obs.Pressures = append(obs.Pressures, value)
	}

	result, err := s.plant.Shutdown(r.Context(), obs)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "result": result})
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}
