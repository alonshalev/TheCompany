package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// GET /v1/projects/{projectID}/runs/{runID}/stream
//
// Server-Sent Events stream. The client receives a stream of JSON events
// for the duration of the run. When the run completes, a final "done" event
// is sent and the connection is closed.
//
// Each event is formatted per the SSE spec:
//
//	data: {"type":"node_started","run_id":"...","data":{...}}\n\n
func (s *Server) handleStreamRunImpl(w http.ResponseWriter, r *http.Request) {
	project, _ := projectFromContext(r.Context())
	runIDStr := chi.URLParam(r, "runID")
	runID, err := uuid.Parse(runIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid run ID")
		return
	}

	// Verify the run belongs to this project
	run, err := s.runtime.GetRun(r.Context(), runID)
	if err != nil || run == nil || run.ProjectID != project.ID {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}

	// If the run is already terminal, stream the historical events and close.
	if run.Status == "succeeded" || run.Status == "failed" || run.Status == "cancelled" {
		s.streamHistoricalEvents(w, r, runID)
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering
	w.WriteHeader(http.StatusOK)

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Subscribe to live events
	ch := s.broadcaster.Subscribe(runID)
	defer s.broadcaster.Unsubscribe(runID, ch)

	// Send a heartbeat comment every 15s to keep the connection alive
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	// Timeout: auto-close after 30 minutes if no terminal event arrives
	timeout := time.NewTimer(30 * time.Minute)
	defer timeout.Stop()

	writeSSEComment(w, "connected")
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return

		case <-timeout.C:
			writeSSEData(w, map[string]any{"type": "timeout", "run_id": runID})
			flusher.Flush()
			return

		case <-heartbeat.C:
			writeSSEComment(w, "heartbeat")
			flusher.Flush()

		case event, open := <-ch:
			if !open {
				return
			}
			writeSSEData(w, event)
			flusher.Flush()

			// Close the stream on terminal events
			t := event.Type
			if t == "run_succeeded" || t == "run_failed" || t == "run_cancelled" {
				writeSSEData(w, map[string]any{"type": "done", "run_id": runID})
				flusher.Flush()
				return
			}
		}
	}
}

// streamHistoricalEvents sends the stored event log for a completed run and closes.
func (s *Server) streamHistoricalEvents(w http.ResponseWriter, r *http.Request, runID uuid.UUID) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}

	events, err := s.runtime.GetRunEvents(r.Context(), runID)
	if err != nil {
		writeSSEData(w, map[string]any{"type": "error", "error": "failed to load events"})
		flusher.Flush()
		return
	}

	for _, e := range events {
		writeSSEData(w, map[string]any{
			"type":    e.Kind,
			"run_id":  e.RunID,
			"seq":     e.Seq,
			"payload": e.Payload,
			"ts":      e.CreatedAt,
		})
	}
	writeSSEData(w, map[string]any{"type": "done", "run_id": runID})
	flusher.Flush()
}

func writeSSEData(w http.ResponseWriter, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", b)
}

func writeSSEComment(w http.ResponseWriter, comment string) {
	fmt.Fprintf(w, ": %s\n\n", comment)
}
