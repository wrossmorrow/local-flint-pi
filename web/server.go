package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Server serves the pi web interface: an embedded single-page UI plus a small
// JSON API over the RpcClient's event stream.
type Server struct {
	rpc *RpcClient
}

func NewServer(rpc *RpcClient) *Server {
	return &Server{rpc: rpc}
}

// Routes returns the HTTP handler for the web interface.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/stats", s.handleStats)
	mux.HandleFunc("/api/prompt", s.handlePrompt)
	mux.HandleFunc("/api/abort", s.handleAbort)
	mux.HandleFunc("/api/clear_queue", s.handleClearQueue)
	mux.HandleFunc("/api/events", s.handleEvents)
	mux.HandleFunc("/api/extension-ui/response", s.handleExtensionUIResponse)
	return mux
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	http.FileServer(newUIServer()).ServeHTTP(w, r)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	state, err := s.rpc.State(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	stats, err := s.rpc.Stats(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) handlePrompt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
		return
	}
	var req struct {
		Message string           `json:"message"`
		Images  []map[string]any `json:"images"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad_request"})
		return
	}
	if req.Message == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "empty_message"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), defaultCommandTimeout)
	defer cancel()
	disposition, err := s.rpc.Prompt(ctx, req.Message, req.Images)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"disposition": disposition})
}

func (s *Server) handleAbort(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), defaultCommandTimeout)
	defer cancel()
	if _, err := s.rpc.Send(ctx, map[string]any{"type": "abort"}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (s *Server) handleClearQueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), defaultCommandTimeout)
	defer cancel()
	res, err := s.rpc.Send(ctx, map[string]any{"type": "clear_queue"})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if !res.Success {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": res.Error})
		return
	}
	steering, _ := res.Data["steering"].([]any)
	followUp, _ := res.Data["followUp"].([]any)
	writeJSON(w, http.StatusOK, map[string]any{
		"steering": steering,
		"followUp": followUp,
	})
}

// handleEvents streams RPC session events to the client as server-sent events.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, unsub := s.rpc.Subscribe()
	defer unsub()

	ctx := r.Context()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(ev)
			if err != nil {
				return
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
		case <-ctx.Done():
			return
		}
	}
}

// handleExtensionUIResponse answers a pending extension UI dialog request.
func (s *Server) handleExtensionUIResponse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
		return
	}
	var req struct {
		ID        string `json:"id"`
		Value     string `json:"value"`
		Confirmed *bool  `json:"confirmed"`
		Cancelled *bool  `json:"cancelled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad_request"})
		return
	}
	if req.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing_id"})
		return
	}
	response := map[string]any{"type": "extension_ui_response", "id": req.ID}
	switch {
	case req.Cancelled != nil && *req.Cancelled:
		response["cancelled"] = true
	case req.Confirmed != nil:
		response["confirmed"] = *req.Confirmed
	case req.Value != "":
		response["value"] = req.Value
	}
	if err := s.rpc.writeJSON(response); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, "encode error", http.StatusInternalServerError)
	}
}
