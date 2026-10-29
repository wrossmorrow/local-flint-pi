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
	"log"
	"os/exec"
	"sync"
	"time"
)

const (
	// defaultCommandTimeout bounds how long a command may take to produce its
	// correlated response.
	defaultCommandTimeout = 30 * time.Second
	// subscriberBuffer is how many events a subscriber may fall behind before
	// it is disconnected. A disconnected browser reconnects and resyncs from
	// get_messages, which is cheaper and more correct than silently dropping
	// streaming deltas.
	subscriberBuffer = 4096
	// maxLineBytes bounds a single JSONL record from the child.
	maxLineBytes = 64 * 1024 * 1024
)

// RpcCommandResult is the response to an RPC command. Success is false when
// the command was rejected; Error carries the failure message. Data is kept
// raw so fields pi omits (sessionName, contextUsage, ...) pass through as-is.
type RpcCommandResult struct {
	Success bool            `json:"success"`
	Error   string          `json:"error,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Event is one session event from the child, forwarded to browsers verbatim
// (modulo slimming, see slimEvent).
type Event = map[string]any

// RpcClient manages a long-lived `pi --mode rpc` child process and fans session
// events out to subscribers.
type RpcClient struct {
	bin  string
	args []string
	env  []string

	writeMu sync.Mutex
	stdin   io.WriteCloser
	cmd     *exec.Cmd

	mu   sync.Mutex
	dead bool

	pendingMu sync.Mutex
	pending   map[string]chan RpcCommandResult
	seq       int64

	// subsMu guards subscribers and the replay state below, so a new
	// subscriber gets a consistent snapshot followed by every later event.
	subsMu  sync.Mutex
	subs    map[chan Event]struct{}
	dialogs map[string]Event // open dialog requests, by id
	order   []string         // dialog ids in arrival order
	ui      map[string]Event // latest setStatus/setWidget/setTitle, by key
	queue   Event            // latest queue_update, replayed to new subscribers

	exitCh chan error
}

// NewRpcClient returns a client for the pi binary at `bin` invoked with `args`
// using the environment `env`.
func NewRpcClient(bin string, args []string, env []string) *RpcClient {
	return &RpcClient{
		bin:     bin,
		args:    args,
		env:     env,
		pending: map[string]chan RpcCommandResult{},
		subs:    map[chan Event]struct{}{},
		dialogs: map[string]Event{},
		ui:      map[string]Event{},
		queue:   Event{"type": ""},
		exitCh:  make(chan error, 1),
	}
}

// Start spawns the pi RPC child process.
func (c *RpcClient) Start() error {
	cmd := exec.Command(c.bin, c.args...)
	cmd.Env = c.env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	c.cmd = cmd
	c.stdin = stdin

	go func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			log.Printf("pi: %s", sc.Text())
		}
	}()
	go func() {
		c.readLoop(stdout)
		err := cmd.Wait()
		c.handleExit(err)
		c.exitCh <- err
	}()
	return nil
}

// Wait blocks until the child process exits and returns its exit error.
func (c *RpcClient) Wait() error { return <-c.exitCh }

// Stop terminates the child process.
func (c *RpcClient) Stop() error {
	if c.cmd == nil || c.cmd.Process == nil {
		return nil
	}
	return c.cmd.Process.Kill()
}

func (c *RpcClient) isDead() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dead
}

// handleExit marks the client dead, fails outstanding commands, and closes
// subscriber channels so HTTP handlers and SSE clients wind down.
func (c *RpcClient) handleExit(err error) {
	c.mu.Lock()
	c.dead = true
	c.mu.Unlock()

	msg := "pi rpc process exited"
	if err != nil {
		msg += ": " + err.Error()
	}
	c.pendingMu.Lock()
	pending := c.pending
	c.pending = map[string]chan RpcCommandResult{}
	c.pendingMu.Unlock()
	for _, ch := range pending {
		ch <- RpcCommandResult{Success: false, Error: msg}
	}

	c.subsMu.Lock()
	for ch := range c.subs {
		delete(c.subs, ch)
		close(ch)
	}
	c.subsMu.Unlock()
}

// readLoop consumes stdout JSONL records until EOF. Records are split on LF
// only (bufio.ScanLines also strips a trailing CR), per the RPC framing rules.
func (c *RpcClient) readLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), maxLineBytes)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var raw struct {
			Type    string          `json:"type"`
			ID      string          `json:"id"`
			Success bool            `json:"success"`
			Error   string          `json:"error"`
			Data    json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(line, &raw); err != nil {
			continue // diagnostics are not protocol records
		}
		if raw.Type == "response" {
			c.resolve(raw.ID, RpcCommandResult{Success: raw.Success, Error: raw.Error, Data: raw.Data})
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		c.broadcast(slimEvent(ev))
	}
	if err := sc.Err(); err != nil {
		log.Printf("pi rpc read loop stopped: %v", err)
	}
}

func (c *RpcClient) resolve(id string, res RpcCommandResult) {
	c.pendingMu.Lock()
	ch, ok := c.pending[id]
	delete(c.pending, id)
	c.pendingMu.Unlock()
	if ok {
		ch <- res // buffered(1); never blocks
	}
}

// slimEvent drops the cumulative `message`/`partial` snapshots pi attaches to
// every message_update. They re-send the whole message on every token, which
// is O(n^2) bytes over SSE for long answers. The browser accumulates deltas
// and replaces its copy with the authoritative message at message_end. For
// toolcall_* updates the block being streamed is kept (as `block`) so the
// browser knows the tool name and call id early.
func slimEvent(ev Event) Event {
	if ev["type"] != "message_update" {
		return ev
	}
	delete(ev, "message")
	ame, ok := ev["assistantMessageEvent"].(map[string]any)
	if !ok {
		return ev
	}
	if partial, ok := ame["partial"].(map[string]any); ok {
		if idx, ok := ame["contentIndex"].(float64); ok {
			if content, ok := partial["content"].([]any); ok && int(idx) < len(content) && int(idx) >= 0 {
				if block, ok := content[int(idx)].(map[string]any); ok && block["type"] == "toolCall" {
					ame["block"] = block
				}
			}
		}
		delete(ame, "partial")
	}
	return ev
}

// broadcast records replayable UI state and fans the event out. A subscriber
// whose buffer is full is disconnected rather than silently missing events.
func (c *RpcClient) broadcast(ev Event) {
	c.subsMu.Lock()
	defer c.subsMu.Unlock()
	c.remember(ev)
	for ch := range c.subs {
		select {
		case ch <- ev:
		default:
			delete(c.subs, ch)
			close(ch)
		}
	}
}

// remember tracks state a freshly (re)connected browser needs that it cannot
// get from any RPC command: open dialogs and extension status/widget/title, and
// the latest steering/follow-up queue.
func (c *RpcClient) remember(ev Event) {
	if ev["type"] == "queue_update" {
		c.queue = ev
		return
	}
	if ev["type"] != "extension_ui_request" {
		return
	}
	id, _ := ev["id"].(string)
	method, _ := ev["method"].(string)
	switch method {
	case "select", "confirm", "input", "editor":
		if _, ok := c.dialogs[id]; !ok {
			c.order = append(c.order, id)
		}
		c.dialogs[id] = ev
		// pi resolves timed-out dialogs itself without telling us; stop
		// replaying (and close it in browsers) once that has happened.
		if ms, ok := ev["timeout"].(float64); ok && ms > 0 {
			time.AfterFunc(time.Duration(ms)*time.Millisecond, func() { c.forgetDialog(id) })
		}
	case "setStatus":
		key, _ := ev["statusKey"].(string)
		c.rememberUI("status:"+key, ev, ev["statusText"] == nil)
	case "setWidget":
		key, _ := ev["widgetKey"].(string)
		c.rememberUI("widget:"+key, ev, ev["widgetLines"] == nil)
	case "setTitle":
		c.rememberUI("title", ev, false)
	}
}

func (c *RpcClient) rememberUI(key string, ev Event, clear bool) {
	if clear {
		delete(c.ui, key)
	} else {
		c.ui[key] = ev
	}
}

// forgetDialog drops a dialog once it is answered and tells every browser, so
// other open tabs close it too.
func (c *RpcClient) forgetDialog(id string) {
	c.subsMu.Lock()
	_, ok := c.dialogs[id]
	delete(c.dialogs, id)
	for i, v := range c.order {
		if v == id {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
	c.subsMu.Unlock()
	if ok {
		c.broadcast(Event{"type": "web_dialog_closed", "id": id})
	}
}

// Subscribe registers a new event channel, preloaded with the replay state.
// The returned function unsubscribes; the channel is closed on unsubscribe,
// child exit, or when the subscriber falls too far behind.
func (c *RpcClient) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, subscriberBuffer)
	c.subsMu.Lock()
	for _, ev := range c.ui {
		ch <- ev
	}
	for _, id := range c.order {
		ch <- c.dialogs[id]
	}
	if c.queue["type"] != "" {
		ch <- c.queue
	}
	if c.isDead() {
		close(ch)
	} else {
		c.subs[ch] = struct{}{}
	}
	c.subsMu.Unlock()
	return ch, func() {
		c.subsMu.Lock()
		if _, ok := c.subs[ch]; ok {
			delete(c.subs, ch)
			close(ch)
		}
		c.subsMu.Unlock()
	}
}

// Send writes one command (with a client-generated correlation id) and waits
// for its response.
func (c *RpcClient) Send(ctx context.Context, command map[string]any) (*RpcCommandResult, error) {
	if c.isDead() {
		return nil, fmt.Errorf("pi rpc process is not running")
	}
	c.pendingMu.Lock()
	c.seq++
	id := fmt.Sprintf("web_%d", c.seq)
	ch := make(chan RpcCommandResult, 1)
	c.pending[id] = ch
	c.pendingMu.Unlock()

	full := make(map[string]any, len(command)+1)
	for k, v := range command {
		full[k] = v
	}
	full["id"] = id
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

// State sends the get_state command and returns the response data payload
// (the session file and friends). It is how the server learns which session
// file it should be uploading.
func (c *RpcClient) State(ctx context.Context) (map[string]any, error) {
	res, err := c.Send(ctx, map[string]any{"type": "get_state"})
	if err != nil {
		return nil, err
	}
	if !res.Success {
		return nil, fmt.Errorf("get_state failed: %s", res.Error)
	}
	var data map[string]any
	if len(res.Data) > 0 {
		if err := json.Unmarshal(res.Data, &data); err != nil {
			return nil, fmt.Errorf("get_state: bad data: %w", err)
		}
	}
	return data, nil
}

// Stats sends the get_session_stats command and returns the response data
// (token and message totals).
func (c *RpcClient) Stats(ctx context.Context) (map[string]any, error) {
	res, err := c.Send(ctx, map[string]any{"type": "get_session_stats"})
	if err != nil {
		return nil, err
	}
	if !res.Success {
		return nil, fmt.Errorf("get_session_stats failed: %s", res.Error)
	}
	var data map[string]any
	if len(res.Data) > 0 {
		if err := json.Unmarshal(res.Data, &data); err != nil {
			return nil, fmt.Errorf("get_session_stats: bad data: %w", err)
		}
	}
	return data, nil
}

// DialogOpen reports whether a dialog is currently pending for `id`.
func (c *RpcClient) DialogOpen(id string) bool {
	c.subsMu.Lock()
	defer c.subsMu.Unlock()
	_, ok := c.dialogs[id]
	return ok
}

// RespondDialog answers an open extension UI dialog. pi itself applies any
// dialog timeout, so the server keeps no timers of its own.
func (c *RpcClient) RespondDialog(response map[string]any) error {
	id, _ := response["id"].(string)
	response["type"] = "extension_ui_response"
	if err := c.writeJSON(response); err != nil {
		return err
	}
	c.forgetDialog(id)
	return nil
}

// writeJSON serializes v to the child's stdin as one JSONL record. Writes are
// serialized so concurrent HTTP handlers cannot interleave partial lines.
func (c *RpcClient) writeJSON(v any) error {
	line, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.stdin == nil {
		return fmt.Errorf("pi rpc stdin is not open")
	}
	if _, err := c.stdin.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("writing to pi rpc stdin: %w", err)
	}
	return nil
}
