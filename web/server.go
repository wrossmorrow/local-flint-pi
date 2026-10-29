package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// allowedCommands are the RPC commands the browser may send through /api/cmd.
// Anything that only makes sense in a terminal, or that writes files at
// arbitrary paths (export_html), is left out.
var allowedCommands = setOf(
	"prompt", "steer", "follow_up", "abort", "new_session",
	"get_state", "get_messages", "get_session_stats", "get_commands",
	"get_available_models", "set_model", "cycle_model",
	"get_available_thinking_levels", "set_thinking_level", "cycle_thinking_level",
	"set_steering_mode", "set_follow_up_mode",
	"compact", "set_auto_compaction", "set_auto_retry", "abort_retry",
	"bash", "abort_bash",
	"set_session_name", "get_last_assistant_text",
	"get_fork_messages", "fork", "clone",
)

func setOf(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, it := range items {
		m[it] = true
	}
	return m
}

// longCommands may legitimately run for minutes (a bash command, a compaction).
var longCommands = setOf("bash", "compact")

// Server serves the pi web interface: the embedded UI plus a small JSON API
// over the RpcClient.
type Server struct {
	rpc          *RpcClient
	allowedHosts map[string]bool
	logoPath     string
	uploader     *sessionUploader
}

func NewServer(rpc *RpcClient, allowedHosts []string, logoPath string, uploader *sessionUploader) *Server {
	hosts := map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}
	for _, h := range allowedHosts {
		if h = strings.TrimSpace(h); h != "" {
			hosts[strings.ToLower(h)] = true
		}
	}
	s := &Server{rpc: rpc, allowedHosts: hosts, logoPath: logoPath}
	if uploader == nil {
		uploader = newSessionUploader(nil, time.Hour, defaultSessionMaxPartBytes, func(context.Context) (*sessionRef, error) { return &sessionRef{Path: ""}, nil })
	}
	s.uploader = uploader
	return s
}

// SessionSource returns the function the session uploader uses to resolve the
// live session. The server owns the RpcClient, so it is the natural source of
// truth for which session is live.
func (s *Server) SessionSource() func(context.Context) (*sessionRef, error) {
	return func(ctx context.Context) (*sessionRef, error) { return s.SessionRef(ctx) }
}

// SessionRef resolves the live pi session the uploader ships: its file path,
// the get_state / get_session_stats snapshots, and the static session
// attributes (repo, agent environment, agent identity, client host).
func (s *Server) SessionRef(ctx context.Context) (*sessionRef, error) {
	state, err := s.rpc.State(ctx)
	if err != nil {
		return nil, err
	}
	p, _ := state["sessionFile"].(string)
	if p == "" {
		return nil, fmt.Errorf("pi reported no session file")
	}
	cwd, wdErr := os.Getwd()
	if wdErr != nil {
		cwd = ""
	}
	ref := &sessionRef{
		Path:             p,
		State:            state,
		Stats:            map[string]any{},
		Repo:             gitRepo(cwd),
		AgentEnvironment: agentEnvironment(),
		AgentIdentity:    os.Getenv("GITHUB_APP_USER"),
		ClientHost:       clientHost(),
	}
	if stats, err := s.rpc.Stats(ctx); err == nil {
		ref.Stats = stats
	}
	return ref, nil
}

// gitRepo returns the origin remote (owner/name) of the git repo at dir, if
// any.
func gitRepo(dir string) string {
	out, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	u := strings.TrimSpace(string(out))
	u = strings.TrimSuffix(u, ".git")
	if i := strings.LastIndex(u, "/"); i >= 0 {
		return u[i+1:]
	}
	return u
}

// agentEnvironment collects the agent runtime details (no secrets) for the
// session metadata. Values are supplied via env and are optional.
func agentEnvironment() map[string]any {
	env := map[string]any{}
	if v := os.Getenv("PI_WEB_AGENT_TOOLCHAIN"); v != "" {
		env["toolchain"] = v
	}
	if v := os.Getenv("PI_WEB_AGENT_IMAGE_DIGEST"); v != "" {
		env["imageDigest"] = v
	}
	if v := os.Getenv("PI_WEB_AGENT_PI_VERSION"); v != "" {
		env["piVersion"] = v
	}
	if v := os.Getenv("PI_WEB_AGENT_EXTENSION_VERSIONS"); v != "" {
		env["extensionVersions"] = v
	}
	return env
}

// clientHost returns the hostname of this machine, used as a coarse client
// identifier in the session metadata.
func clientHost() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "unknown"
}

// Routes returns the HTTP handler for the web interface.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServer(uiFS()))
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/info", s.handleInfo)
	mux.HandleFunc("GET /api/logo", s.handleLogo)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("GET /api/state", s.passthrough("get_state"))
	mux.HandleFunc("GET /api/stats", s.passthrough("get_session_stats"))
	mux.HandleFunc("POST /api/cmd", s.handleCommand)
	mux.HandleFunc("POST /api/extension-ui/response", s.handleExtensionUIResponse)
	mux.HandleFunc("POST /api/flush", s.handleFlush)
	return s.guard(mux)
}

// guard rejects requests that could come from another website. The server is
// only ever meant to be opened as http://localhost:<port>, but any page the
// browser has open can send requests to localhost too:
//   - the Host check defeats DNS rebinding (evil.example resolving to 127.0.0.1);
//   - requiring a JSON content type on writes forces a CORS preflight, which
//     this server never approves, so cross-site form/no-cors POSTs are refused;
//   - a present Origin header must match the Host.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.Trim(strings.ToLower(host), "[]")
		if !s.allowedHosts[host] {
			http.Error(w, "forbidden host (set PI_WEB_ALLOWED_HOSTS)", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || !strings.EqualFold(u.Host, r.Host) {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				http.Error(w, "content type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	status := http.StatusOK
	if s.rpc.isDead() {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, map[string]any{"ok": status == http.StatusOK})
}

// handleInfo reports things the footer shows that pi's RPC does not expose:
// the working directory and its git branch.
func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	cwd, _ := os.Getwd()
	writeJSON(w, http.StatusOK, map[string]any{"cwd": cwd, "branch": gitBranch(cwd)})
}

func gitBranch(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, ".git", "HEAD"))
	if err != nil {
		return ""
	}
	head := strings.TrimSpace(string(b))
	if ref, ok := strings.CutPrefix(head, "ref: refs/heads/"); ok {
		return ref
	}
	if len(head) >= 7 {
		return head[:7] // detached
	}
	return ""
}

// handleLogo serves the braille logo pixels used by the rec extension's
// terminal header, so the browser can draw the same animated mark.
func (s *Server) handleLogo(w http.ResponseWriter, r *http.Request) {
	if s.logoPath == "" {
		http.NotFound(w, r)
		return
	}
	b, err := os.ReadFile(s.logoPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "max-age=3600")
	_, _ = w.Write(b)
}

func (s *Server) passthrough(command string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.run(w, r, map[string]any{"type": command})
	}
}

// handleCommand forwards one allowlisted RPC command and returns pi's
// response ({success, error?, data?}) unchanged.
func (s *Server) handleCommand(w http.ResponseWriter, r *http.Request) {
	var cmd map[string]any
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20)).Decode(&cmd); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "bad request: " + err.Error()})
		return
	}
	t, _ := cmd["type"].(string)
	if !allowedCommands[t] {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": fmt.Sprintf("command %q not allowed", t)})
		return
	}
	delete(cmd, "id")
	s.run(w, r, cmd)
}

func (s *Server) run(w http.ResponseWriter, r *http.Request, cmd map[string]any) {
	timeout := defaultCommandTimeout
	if t, _ := cmd["type"].(string); longCommands[t] {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	res, err := s.rpc.Send(ctx, cmd)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"success": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleFlush ships everything in the current session since the last offset
// and reports what was uploaded. The UI can call it to force an upload now;
// the periodic ticker and shutdown close do the same automatically.
func (s *Server) handleFlush(w http.ResponseWriter, r *http.Request) {
	if !s.uploader.Enabled() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "error": "s3 not configured"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), defaultCommandTimeout)
	defer cancel()
	ref, err := s.SessionRef(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "error": "session unavailable: " + err.Error()})
		return
	}
	st, err := s.uploader.Flush(ctx, ref)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"success": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":     true,
		"sessionKey":  st.sessionKey,
		"finalOffset": st.lastOffset,
		"partCount":   st.partCount,
		"hash":        st.hashHex(),
		"shipped":     st.shipped,
	})
}

// handleEvents streams RPC session events to the client as server-sent events.
// The stream starts with any open dialogs and extension status/widgets, and
// ends if the browser falls too far behind; EventSource then reconnects and
// the UI resyncs from get_messages.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "retry: 1000\n\n")
	flusher.Flush()

	ch, unsub := s.rpc.Subscribe()
	defer unsub()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()

	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return
			}
			// Drain whatever else is already queued before flushing, so a
			// burst of token deltas goes out in one write.
		drain:
			for drained := 0; drained < 256; drained++ {
				select {
				case ev, ok := <-ch:
					if !ok {
						flusher.Flush()
						return
					}
					if data, err := json.Marshal(ev); err == nil {
						fmt.Fprintf(w, "data: %s\n\n", data)
					}
				default:
					break drain
				}
			}
			flusher.Flush()
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// handleExtensionUIResponse answers an open extension UI dialog.
func (s *Server) handleExtensionUIResponse(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID        string  `json:"id"`
		Value     *string `json:"value"`
		Confirmed *bool   `json:"confirmed"`
		Cancelled bool    `json:"cancelled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20)).Decode(&req); err != nil || req.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "bad request"})
		return
	}
	if !s.rpc.DialogOpen(req.ID) {
		writeJSON(w, http.StatusNotFound, map[string]any{"success": false, "error": "unknown dialog"})
		return
	}
	response := map[string]any{"id": req.ID}
	switch {
	case req.Cancelled:
		response["cancelled"] = true
	case req.Confirmed != nil:
		response["confirmed"] = *req.Confirmed
	case req.Value != nil:
		response["value"] = *req.Value
	default:
		response["cancelled"] = true
	}
	if err := s.rpc.RespondDialog(response); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"success": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
