// Package main implements a small HTTP + Server-Sent-Events web interface for a
// long-lived `pi --mode rpc` child process.
//
// The wire protocol is JSONL over the child's stdin/stdout (see
// https://pi.dev/docs/latest/rpc): one JSON object per line. Commands are
// written to stdin with an `id`; responses echo the id and carry
// `{success, error?, data?}`. Everything else on stdout is a session event
// (or an `extension_ui_request` awaiting an `extension_ui_response`).
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	// defaultCommandTimeout bounds how long a command may take to produce its
	// correlated response.
	defaultCommandTimeout = 30 * time.Second
	// defaultDialogTimeout bounds how long an extension UI dialog waits for a
	// client response before the agent auto-resolves it.
	defaultDialogTimeout = 30 * time.Second
)

// RpcCommandResult is the response to an RPC command. Success is false when
// the command was rejected; Error carries the failure message.
type RpcCommandResult struct {
	Success bool           `json:"success"`
	Error   string         `json:"error,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
}

// RpcClient manages a long-lived `pi --mode rpc` child process and fans session
// events out to subscribers.
type RpcClient struct {
	bin  string
	args []string
	env  []string

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	scanner *bufio.Scanner
	stderr  strings.Builder
	dead    bool

	pendingMu sync.Mutex
	pending   map[string]chan RpcCommandResult

	subsMu sync.Mutex
	subs   map[chan map[string]any]struct{}

	dialogsMu sync.Mutex
	dialogs   map[string]*pendingDialog

	exitCh chan error
	seq    int64
}

// pendingDialog is one in-flight extension UI dialog request. The agent waits
// for an `extension_ui_response` carrying the request id before continuing.
type pendingDialog struct {
	method  string
	timeout time.Duration
	timer   *time.Timer
}

// NewRpcClient returns a client for the pi binary at `bin` invoked with `args`
// using the environment `env`.
func NewRpcClient(bin string, args []string, env []string) *RpcClient {
	return &RpcClient{
		bin:     bin,
		args:    args,
		env:     env,
		pending: map[string]chan RpcCommandResult{},
		subs:    map[chan map[string]any]struct{}{},
		dialogs: map[string]*pendingDialog{},
	}
}

// Start spawns the pi RPC child process.
func (c *RpcClient) Start(ctx context.Context) error {
	cmd := exec.Command(c.bin, c.args...)
	cmd.Env = c.env

	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		return err
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	c.cmd = cmd
	c.stdin = stdin
	c.scanner = bufio.NewScanner(stdoutR)
	c.scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)

	exitCh := make(chan error, 1)
	c.exitCh = exitCh
	go func() {
		err := cmd.Wait()
		exitCh <- err
		c.handleExit(err)
	}()
	go c.readLoop()
	go func() {
		sc := bufio.NewScanner(stderrR)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			c.setStderr(sc.Text())
		}
		c.setStderr("")
	}()

	// Brief readiness wait before serving commands, mirroring the upstream
	// RpcClient behavior of letting the child settle before first use.
	time.Sleep(100 * time.Millisecond)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dead {
		return fmt.Errorf("pi rpc process exited during startup")
	}
	return nil
}

func (c *RpcClient) setStderr(s string) {
	c.mu.Lock()
	c.stderr.WriteString(s)
	c.mu.Unlock()
}

func (c *RpcClient) stderrText() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stderr.String()
}

func (c *RpcClient) isDead() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dead
}

// Wait blocks until the child process exits and returns its exit error.
func (c *RpcClient) Wait() error {
	return <-c.exitCh
}

// Stop terminates the child process. The read loop observes the exit and
// drives the rest of the shutdown sequence.
func (c *RpcClient) Stop(ctx context.Context) error {
	c.mu.Lock()
	cmd := c.cmd
	c.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

// handleExit marks the client dead, fails outstanding commands, and closes
// subscriber channels so HTTP handlers and SSE clients wind down.
func (c *RpcClient) handleExit(err error) {
	c.mu.Lock()
	c.dead = true
	c.mu.Unlock()

	c.failPending(err)

	c.subsMu.Lock()
	subs := make([]chan map[string]any, 0, len(c.subs))
	for ch := range c.subs {
		subs = append(subs, ch)
	}
	for ch := range c.subs {
		delete(c.subs, ch)
	}
	c.subsMu.Unlock()
	for _, ch := range subs {
		close(ch)
	}
}

// readLoop consumes stdout JSONL records until EOF, then waits on the child.
func (c *RpcClient) readLoop() {
	for c.scanner.Scan() {
		line := c.scanner.Text()
		if line == "" {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue // diagnostics/blank lines are not protocol records
		}
		c.handleLine(raw)
	}
	c.mu.Lock()
	if !c.dead {
		c.dead = true
	}
	c.mu.Unlock()
	_ = c.cmd.Wait()
}

// Subscribe registers a new event channel and returns it with an unsubscribe
// function. The channel is closed when the client exits or the subscriber
// unsubscribes.
func (c *RpcClient) Subscribe() (<-chan map[string]any, func()) {
	ch := make(chan map[string]any, 64)
	c.subsMu.Lock()
	c.subs[ch] = struct{}{}
	c.subsMu.Unlock()
	return ch, func() {
		c.subsMu.Lock()
		delete(c.subs, ch)
		c.subsMu.Unlock()
		close(ch)
	}
}

func (c *RpcClient) broadcast(ev map[string]any) {
	c.subsMu.Lock()
	subs := make([]chan map[string]any, 0, len(c.subs))
	for ch := range c.subs {
		subs = append(subs, ch)
	}
	c.subsMu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- ev:
		default:
			// Subscriber is slow or gone; drop the event rather than stall the
			// child process's event loop.
		}
	}
}

// Send writes one command (with a client-generated correlation id) and waits
// for its response.
func (c *RpcClient) Send(ctx context.Context, command map[string]any) (*RpcCommandResult, error) {
	if c.isDead() {
		return nil, fmt.Errorf("pi rpc client is not running")
	}
	var id string
	c.pendingMu.Lock()
	c.seq++
	id = fmt.Sprintf("req_%d", c.seq)
	ch := make(chan RpcCommandResult, 1)
	c.pending[id] = ch
	c.pendingMu.Unlock()

	full := map[string]any{"id": id}
	for k, v := range command {
		full[k] = v
	}
	if err := c.writeJSON(full); err != nil {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
		return nil, err
	}
	select {
	case res := <-ch:
		return &res, nil
	case <-ctx.Done():
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
		return nil, ctx.Err()
	}
}

// writeJSON serializes command to the child's stdin as one JSONL record.
func (c *RpcClient) writeJSON(v any) error {
	line, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if c.stdin == nil {
		return fmt.Errorf("pi rpc child stdin is not open")
	}
	if _, err := c.stdin.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("failed writing to pi rpc stdin: %w", err)
	}
	return nil
}

func (c *RpcClient) failPending(err error) {
	c.pendingMu.Lock()
	pending := c.pending
	c.pending = map[string]chan RpcCommandResult{}
	c.pendingMu.Unlock()
	for _, ch := range pending {
		select {
		case ch <- RpcCommandResult{Success: false, Error: err.Error()}:
		default:
		}
		close(ch)
	}
}

// handleLine dispatches one decoded JSONL record.
func (c *RpcClient) handleLine(raw map[string]any) {
	t, _ := raw["type"].(string)
	switch t {
	case "extension_ui_response":
		c.handleExtensionUIResponse(raw)
		return
	case "response":
		c.handleResponse(raw)
		return
	}
	c.broadcast(raw)
	if t == "extension_ui_request" {
		c.handleExtensionUIRequest(raw)
	}
}

func (c *RpcClient) handleResponse(raw map[string]any) {
	id, _ := raw["id"].(string)
	c.pendingMu.Lock()
	ch, ok := c.pending[id]
	delete(c.pending, id)
	c.pendingMu.Unlock()
	if !ok {
		return
	}
	success, _ := raw["success"].(bool)
	result := &RpcCommandResult{Success: success}
	if !success {
		result.Error, _ = raw["error"].(string)
	}
	result.Data = map[string]any{}
	if data, ok := raw["data"].(map[string]any); ok {
		result.Data = data
	}
	select {
	case ch <- *result:
	default:
	}
	close(ch)
}

// handleExtensionUIRequest registers (or refreshes) the timeout for dialog
// methods and broadcasts the request to subscribers. Fire-and-forget methods
// (notify, setStatus, setWidget, setTitle, set_editor_text) need no response.
func (c *RpcClient) handleExtensionUIRequest(raw map[string]any) {
	method, _ := raw["method"].(string)
	id, _ := raw["id"].(string)
	switch method {
	case "select", "confirm", "input", "editor":
		c.dialogsMu.Lock()
		if _, exists := c.dialogs[id]; !exists {
			timeout := defaultDialogTimeout
			if ms, ok := raw["timeout"].(float64); ok {
				timeout = time.Duration(ms) * time.Millisecond
			}
			c.dialogs[id] = &pendingDialog{method: method, timeout: timeout}
			go func() {
				timer := time.NewTimer(timeout)
				defer timer.Stop()
				<-timer.C
				c.resolveDialog(id, map[string]any{"cancelled": true})
			}()
		}
		c.dialogsMu.Unlock()
	}
}

// isCancelled reports whether a dialog response was cancelled.
func isCancelled(response map[string]any) bool {
	cancelled, ok := response["cancelled"].(bool)
	return ok && cancelled
}

// resolveDialog answers the pending dialog for `id` with `response`.
func (c *RpcClient) resolveDialog(id string, response map[string]any) {
	c.dialogsMu.Lock()
	d, ok := c.dialogs[id]
	delete(c.dialogs, id)
	c.dialogsMu.Unlock()
	if !ok {
		return
	}
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}

	cmd := map[string]any{"type": "extension_ui_response", "id": id}
	switch d.method {
	case "select":
		if !isCancelled(response) {
			cmd["value"] = response["value"]
		}
	case "confirm":
		if !isCancelled(response) {
			cmd["confirmed"] = response["confirmed"].(bool)
		}
	case "input", "editor":
		if !isCancelled(response) {
			cmd["value"] = response["value"]
		}
	}
	if err := c.writeJSON(cmd); err != nil {
		return
	}
}

// handleExtensionUIResponse answers the pending extension UI dialog with the
// matching id, if any, using the response just received from the client.
func (c *RpcClient) handleExtensionUIResponse(raw map[string]any) {
	id, _ := raw["id"].(string)
	c.resolveDialog(id, raw)
}

// State is the snapshot returned by the get_state command.
type State struct {
	Model                 map[string]any `json:"model"`
	ThinkingLevel         string         `json:"thinkingLevel"`
	IsStreaming           bool           `json:"isStreaming"`
	IsCompacting          bool           `json:"isCompacting"`
	SteeringMode          string         `json:"steeringMode"`
	FollowUpMode          string         `json:"followUpMode"`
	SessionFile           string         `json:"sessionFile"`
	SessionID             string         `json:"sessionId"`
	SessionName           string         `json:"sessionName"`
	AutoCompactionEnabled bool           `json:"autoCompactionEnabled"`
	MessageCount          int            `json:"messageCount"`
	PendingMessageCount   int            `json:"pendingMessageCount"`
}

// Stats is the snapshot returned by the get_session_stats command.
type Stats struct {
	SessionFile       string         `json:"sessionFile"`
	SessionID         string         `json:"sessionId"`
	UserMessages      int            `json:"userMessages"`
	AssistantMessages int            `json:"assistantMessages"`
	ToolCalls         int            `json:"toolCalls"`
	ToolResults       int            `json:"toolResults"`
	TotalMessages     int            `json:"totalMessages"`
	Tokens            map[string]any `json:"tokens"`
	Cost              float64        `json:"cost"`
	ContextUsage      map[string]any `json:"contextUsage"`
}

// State fetches the current session state via the get_state command.
func (c *RpcClient) State(ctx context.Context) (*State, error) {
	res, err := c.Send(ctx, map[string]any{"type": "get_state"})
	if err != nil {
		return nil, err
	}
	if !res.Success {
		return nil, fmt.Errorf("get_state failed: %s", res.Error)
	}
	data := res.Data
	if data == nil {
		return nil, fmt.Errorf("get_state returned no data")
	}
	s := &State{}
	if v, ok := data["model"].(map[string]any); ok {
		s.Model = v
	}
	s.ThinkingLevel = data["thinkingLevel"].(string)
	s.IsStreaming = data["isStreaming"].(bool)
	s.IsCompacting = data["isCompacting"].(bool)
	s.SteeringMode = data["steeringMode"].(string)
	s.FollowUpMode = data["followUpMode"].(string)
	s.SessionFile = data["sessionFile"].(string)
	s.SessionID = data["sessionId"].(string)
	s.SessionName = data["sessionName"].(string)
	s.AutoCompactionEnabled = data["autoCompactionEnabled"].(bool)
	s.MessageCount = int(data["messageCount"].(float64))
	s.PendingMessageCount = int(data["pendingMessageCount"].(float64))
	return s, nil
}

// Stats fetches session statistics via the get_session_stats command.
func (c *RpcClient) Stats(ctx context.Context) (*Stats, error) {
	res, err := c.Send(ctx, map[string]any{"type": "get_session_stats"})
	if err != nil {
		return nil, err
	}
	if !res.Success {
		return nil, fmt.Errorf("get_session_stats failed: %s", res.Error)
	}
	data := res.Data
	if data == nil {
		return nil, fmt.Errorf("get_session_stats returned no data")
	}
	s := &Stats{}
	s.SessionFile = data["sessionFile"].(string)
	s.SessionID = data["sessionId"].(string)
	s.UserMessages = int(data["userMessages"].(float64))
	s.AssistantMessages = int(data["assistantMessages"].(float64))
	s.ToolCalls = int(data["toolCalls"].(float64))
	s.ToolResults = int(data["toolResults"].(float64))
	s.TotalMessages = int(data["totalMessages"].(float64))
	s.Tokens = data["tokens"].(map[string]any)
	s.ContextUsage = data["contextUsage"].(map[string]any)
	s.Cost = float64(data["cost"].(float64))
	return s, nil
}

// Prompt sends a user prompt to the agent and waits for its disposition. The
// disposition is "started", "queued", or "handled"; streaming continues via
// events.
func (c *RpcClient) Prompt(ctx context.Context, message string, images []map[string]any) (string, error) {
	command := map[string]any{"type": "prompt", "message": message}
	if len(images) > 0 {
		command["images"] = images
	}
	res, err := c.Send(ctx, command)
	if err != nil {
		return "", err
	}
	if !res.Success {
		return "", fmt.Errorf("prompt failed: %s", res.Error)
	}
	disposition, _ := res.Data["disposition"].(string)
	return disposition, nil
}
