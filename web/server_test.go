package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSlimEventDropsSnapshotsKeepsToolBlock(t *testing.T) {
	ev := Event{
		"type":    "message_update",
		"message": map[string]any{"role": "assistant"},
		"assistantMessageEvent": map[string]any{
			"type":         "toolcall_start",
			"contentIndex": float64(1),
			"partial": map[string]any{"content": []any{
				map[string]any{"type": "text", "text": "hi"},
				map[string]any{"type": "toolCall", "id": "call_1", "name": "bash"},
			}},
		},
	}
	out := slimEvent(ev)
	if _, ok := out["message"]; ok {
		t.Fatal("message snapshot not dropped")
	}
	ame := out["assistantMessageEvent"].(map[string]any)
	if _, ok := ame["partial"]; ok {
		t.Fatal("partial snapshot not dropped")
	}
	block, ok := ame["block"].(map[string]any)
	if !ok || block["id"] != "call_1" {
		t.Fatalf("tool block not kept: %v", ame["block"])
	}

	other := Event{"type": "tool_execution_update", "partialResult": map[string]any{}}
	if _, ok := slimEvent(other)["partialResult"]; !ok {
		t.Fatal("non message_update events must pass through untouched")
	}
}

func TestRememberReplaysOpenDialogsAndStatus(t *testing.T) {
	c := NewRpcClient("", nil, nil)
	c.broadcast(Event{"type": "extension_ui_request", "id": "s", "method": "setStatus", "statusKey": "k", "statusText": "v"})
	c.broadcast(Event{"type": "extension_ui_request", "id": "d1", "method": "confirm", "title": "ok?"})
	c.broadcast(Event{"type": "extension_ui_request", "id": "d2", "method": "select", "options": []any{"a"}})
	c.forgetDialog("d1")

	ch, unsub := c.Subscribe()
	defer unsub()
	var got []string
	for len(ch) > 0 {
		ev := <-ch
		got = append(got, ev["id"].(string))
	}
	if strings.Join(got, ",") != "s,d2" {
		t.Fatalf("replay = %v, want [s d2]", got)
	}

	c.broadcast(Event{"type": "extension_ui_request", "id": "s2", "method": "setStatus", "statusKey": "k"}) // clears k
	ch2, unsub2 := c.Subscribe()
	defer unsub2()
	if n := len(ch2); n != 1 {
		t.Fatalf("after clearing status, replay has %d events, want 1 (the dialog)", n)
	}
}

func TestGuard(t *testing.T) {
	s := NewServer(NewRpcClient("", nil, nil), []string{"devbox.local"}, "")
	h := s.guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))

	cases := []struct {
		name, method, host, origin, ctype string
		want                              int
	}{
		{"loopback get", "GET", "127.0.0.1:8787", "", "", 204},
		{"localhost get", "GET", "localhost:8787", "", "", 204},
		{"allowed extra host", "GET", "devbox.local:8787", "", "", 204},
		{"dns rebinding host", "GET", "evil.example:8787", "", "", 403},
		{"json post same origin", "POST", "localhost:8787", "http://localhost:8787", "application/json", 204},
		{"cross-site origin", "POST", "localhost:8787", "http://evil.example", "application/json", 403},
		{"form post (no preflight)", "POST", "localhost:8787", "", "text/plain", 415},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "/api/cmd", strings.NewReader("{}"))
			r.Host = tc.host
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.ctype != "" {
				r.Header.Set("Content-Type", tc.ctype)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}
		})
	}
}

func TestCommandAllowlist(t *testing.T) {
	s := NewServer(NewRpcClient("", nil, nil), nil, "")
	r := httptest.NewRequest("POST", "/api/cmd", strings.NewReader(`{"type":"export_html","outputPath":"/etc/x"}`))
	r.Host = "localhost"
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "not allowed") {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
}
