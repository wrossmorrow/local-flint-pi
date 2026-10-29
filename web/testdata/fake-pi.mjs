#!/usr/bin/env node
// A stand-in for `pi --mode rpc` that speaks enough of the RPC protocol to
// develop the web UI without a model. It streams scripted runs with thinking,
// markdown, tool calls (bash/read/edit/write), dialogs, steering and errors.
//
//   cd web && PI_BIN=node PI_ARGS=testdata/fake-pi.mjs PI_LOGO_PATH=../rec-ext/logo-pixels.json go run .
//
// Prompt keywords change the script: "confirm", "select", "input", "error",
// "long", "abort-me" (slow, to try Esc). FAKE_PI_SPEED scales delays (0 = instant).

import readline from "node:readline";

const speed = Number(process.env.FAKE_PI_SPEED ?? "1");
const sleep = (ms) => new Promise((r) => setTimeout(r, ms * speed));
const out = (o) => process.stdout.write(JSON.stringify(o) + "\n");
const now = () => Date.now();

const models = [
  { id: "flint", name: "Flint", api: "openai-completions", provider: "rec", baseUrl: "http://127.0.0.1:11500/v1", reasoning: true, input: ["text"], contextWindow: 262144, maxTokens: 32768, cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 } },
  { id: "flint-mini", name: "Flint Mini", api: "openai-completions", provider: "rec", baseUrl: "http://127.0.0.1:11500/v1", reasoning: false, input: ["text", "image"], contextWindow: 131072, maxTokens: 16384, cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 } },
];

const S = {
  model: models[0],
  thinkingLevel: "medium",
  isStreaming: false,
  isCompacting: false,
  sessionName: process.argv.includes("--name") ? process.argv[process.argv.indexOf("--name") + 1] : "local-flint-pi",
  sessionId: "sess_" + Math.random().toString(36).slice(2, 10),
  messages: [],
  steering: [],
  followUp: [],
  aborted: false,
  tokens: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
  dialogs: new Map(),
  toolSeq: 0,
};

function respond(id, command, data, error) {
  const r = { type: "response", command, success: !error };
  if (id) r.id = id;
  if (error) r.error = error;
  else if (data !== undefined) r.data = data;
  out(r);
}

function state() {
  const s = {
    model: S.model, thinkingLevel: S.thinkingLevel, isStreaming: S.isStreaming, isCompacting: S.isCompacting,
    steeringMode: "one-at-a-time", followUpMode: "one-at-a-time",
    sessionFile: `/home/node/.pi/agent/sessions/${S.sessionId}.jsonl`, sessionId: S.sessionId,
    autoCompactionEnabled: true, messageCount: S.messages.length, pendingMessageCount: S.steering.length + S.followUp.length,
  };
  if (S.sessionName) s.sessionName = S.sessionName;
  return s;
}

function stats() {
  const count = (role) => S.messages.filter((m) => m.role === role).length;
  const toolCalls = S.messages.filter((m) => m.role === "assistant").flatMap((m) => m.content).filter((b) => b.type === "toolCall").length;
  const ctx = Math.min(S.model.contextWindow, S.tokens.input + S.tokens.output);
  return {
    sessionFile: state().sessionFile, sessionId: S.sessionId,
    userMessages: count("user"), assistantMessages: count("assistant"), toolCalls, toolResults: count("toolResult"),
    totalMessages: S.messages.length, tokens: S.tokens, cost: 0,
    contextUsage: { tokens: ctx, contextWindow: S.model.contextWindow, percent: (100 * ctx) / S.model.contextWindow },
  };
}

function dialog(method, fields) {
  const id = "dlg_" + Math.random().toString(36).slice(2, 8);
  out({ type: "extension_ui_request", id, method, ...fields });
  return new Promise((resolve) => {
    S.dialogs.set(id, resolve);
    if (fields.timeout) setTimeout(() => { if (S.dialogs.delete(id)) resolve({ cancelled: true }); }, fields.timeout);
  });
}

// ---- streaming one assistant message --------------------------------------

async function streamAssistant(blocks, stopReason) {
  const msg = { role: "assistant", content: [], api: S.model.api, provider: S.model.provider, model: S.model.id, usage: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, cost: { total: 0 } }, stopReason: "stop", timestamp: now() };
  out({ type: "message_start", message: structuredClone(msg) });
  const upd = (e) => out({ type: "message_update", message: structuredClone(msg), assistantMessageEvent: { ...e, partial: structuredClone(msg) } });
  upd({ type: "start" });
  for (const b of blocks) {
    if (S.aborted) break;
    const i = msg.content.length;
    if (b.type === "thinking" || b.type === "text") {
      const key = b.type === "thinking" ? "thinking" : "text";
      msg.content.push({ type: b.type, [key]: "" });
      upd({ type: `${b.type}_start`, contentIndex: i });
      for (const chunk of b[key].match(/[\s\S]{1,9}/g) || []) {
        if (S.aborted) break;
        msg.content[i][key] += chunk;
        upd({ type: `${b.type}_delta`, contentIndex: i, delta: chunk });
        await sleep(b.fast ? 2 : 12);
      }
      upd({ type: `${b.type}_end`, contentIndex: i, content: msg.content[i][key] });
    } else if (b.type === "toolCall") {
      const tc = { type: "toolCall", id: "call_" + ++S.toolSeq, name: b.name, arguments: {} };
      msg.content.push(tc);
      upd({ type: "toolcall_start", contentIndex: i });
      const json = JSON.stringify(b.arguments);
      for (const chunk of json.match(/[\s\S]{1,16}/g) || []) {
        upd({ type: "toolcall_delta", contentIndex: i, delta: chunk });
        await sleep(6);
      }
      tc.arguments = b.arguments;
      upd({ type: "toolcall_end", contentIndex: i, toolCall: structuredClone(tc) });
    }
  }
  msg.stopReason = S.aborted ? "aborted" : stopReason;
  const outTok = JSON.stringify(msg.content).length / 4 | 0;
  msg.usage = { input: 1200 + S.tokens.total, output: outTok, cacheRead: S.tokens.total, cacheWrite: 900, cost: { total: 0 } };
  S.tokens.input += 1200; S.tokens.output += outTok; S.tokens.cacheRead += S.tokens.total; S.tokens.cacheWrite += 900;
  S.tokens.total = S.tokens.input + S.tokens.output;
  if (S.aborted) upd({ type: "error", reason: "aborted" });
  else upd({ type: "done", reason: stopReason });
  S.messages.push(msg);
  out({ type: "message_end", message: msg });
  return msg;
}

async function runTool(call, result, { lines = [], isError = false, details } = {}) {
  out({ type: "tool_execution_start", toolCallId: call.id, toolName: call.name, args: call.arguments });
  let acc = "";
  for (const l of lines) {
    if (S.aborted) break;
    acc += l + "\n";
    out({ type: "tool_execution_update", toolCallId: call.id, toolName: call.name, args: call.arguments, partialResult: { content: [{ type: "text", text: acc }], details: {} } });
    await sleep(70);
  }
  const text = S.aborted ? acc + "\n[aborted]" : result ?? acc;
  const res = { content: [{ type: "text", text }], details: details ?? {} };
  out({ type: "tool_execution_end", toolCallId: call.id, toolName: call.name, result: res, isError: isError || S.aborted });
  const tr = { role: "toolResult", toolCallId: call.id, toolName: call.name, content: res.content, details: res.details, isError: isError || S.aborted, timestamp: now() };
  out({ type: "message_start", message: tr });
  S.messages.push(tr);
  out({ type: "message_end", message: tr });
}

async function userMessage(text) {
  const m = { role: "user", content: [{ type: "text", text }], timestamp: now() };
  out({ type: "message_start", message: m });
  S.messages.push(m);
  out({ type: "message_end", message: m });
}

async function deliverSteering() {
  if (!S.steering.length) return false;
  const text = S.steering.shift();
  out({ type: "queue_update", steering: [...S.steering], followUp: [...S.followUp] });
  await userMessage(text);
  return text;
}

// ---- scripted runs ----------------------------------------------------------

const LS = [
  "total 64",
  "drwxr-xr-x  9 node node  4096 Oct 28 19:58 .",
  "drwxr-xr-x  3 node node  4096 Oct 28 19:40 ..",
  "-rw-r--r--  1 node node  1874 Oct 28 19:40 Dockerfile",
  "-rw-r--r--  1 node node  3311 Oct 28 19:40 README.md",
  "-rw-r--r--  1 node node  4102 Oct 28 19:40 compose.yml",
  "drwxr-xr-x  2 node node  4096 Oct 28 19:40 extproc",
  "-rw-r--r--  1 node node  2290 Oct 28 19:40 justfile",
  "drwxr-xr-x  3 node node  4096 Oct 28 19:40 web",
];
const GO_TEST = ["=== RUN   TestStore", "--- PASS: TestStore (0.01s)", "=== RUN   TestUsage", "--- PASS: TestUsage (0.00s)", "=== RUN   TestTiming", "--- PASS: TestTiming (0.02s)", "PASS", "ok  \tgithub.com/wrossmorrow/local-flint-pi/extproc\t0.412s"];
const READ = `package main

import (
	"net/http"
	"time"
)

// Store keeps per-session usage counters.
type Store struct {
	flushEvery time.Duration
	client     *http.Client
}
`;
const DIFF = ` ...
  9 // Store keeps per-session usage counters.
 10 type Store struct {
-11 	flushEvery time.Duration
+11 	flushEvery    time.Duration
+12 	flushOnClose  bool
 13 	client     *http.Client
 14 }
 ...`;

const THINK = "The user wants me to look at the extproc store. First I should list the repository to orient myself, then run the tests to get a baseline before touching anything. After that, read store.go and make the smallest change that adds a flush-on-close flag. I need to make sure I don't break the MotherDuck upload path, which is exercised by motherduck_test.go.";

const FINAL = `Done. I added a \`flushOnClose\` flag to \`Store\` and wired it through \`Close()\`.

## What changed

- **store.go** — new field \`flushOnClose bool\`; \`Close()\` now calls \`flush(ctx)\` first when it's set
- **main.go** — reads \`EXTPROC_FLUSH_ON_CLOSE\` (default \`true\`)

\`\`\`go
func (s *Store) Close(ctx context.Context) error {
	if s.flushOnClose {
		if err := s.flush(ctx); err != nil {
			return fmt.Errorf("flush on close: %w", err)
		}
	}
	return s.db.Close()
}
\`\`\`

| package | before | after |
|---|---|---|
| extproc | 3 pass | 4 pass |
| web | — | — |

> Note: \`go test ./...\` passes, but I didn't run the MotherDuck integration test since it needs a token.

Want me to open a PR?`;

async function run(text) {
  S.isStreaming = true;
  S.aborted = false;
  out({ type: "agent_start" });
  out({ type: "turn_start" });
  await userMessage(text);
  const lower = text.toLowerCase();

  try {
    if (lower.includes("long")) {
      const para = "Streaming a long answer to exercise rendering performance. ".repeat(6) + "\n\n";
      const body = Array.from({ length: 60 }, (_, i) => (i % 10 === 0 ? `### Section ${i / 10 + 1}\n\n` : "") + para + (i % 7 === 0 ? "```js\nconst x = " + i + ";\nconsole.log(x * 2);\n```\n\n" : "")).join("");
      await streamAssistant([{ type: "text", text: body, fast: true }], "stop");
    } else if (lower.includes("hello") || lower.length < 12) {
      await streamAssistant([{ type: "thinking", thinking: "Simple greeting; answer briefly." }, { type: "text", text: "Hi! I'm ready — what should we work on?" }], "stop");
    } else {
      // turn 1: think, list + test
      let m = await streamAssistant([
        { type: "thinking", thinking: THINK },
        { type: "text", text: "Let me look around and get a test baseline first." },
        { type: "toolCall", name: "bash", arguments: { command: "ls -la" } },
        { type: "toolCall", name: "bash", arguments: { command: "cd extproc && go test ./... 2>&1 | tail -20", timeout: 120 } },
      ], "toolUse");
      const calls = m.content.filter((b) => b.type === "toolCall");
      if (!S.aborted) await runTool(calls[0], undefined, { lines: LS });
      if (!S.aborted) await runTool(calls[1], undefined, { lines: lower.includes("abort-me") ? Array.from({ length: 80 }, (_, i) => `slow line ${i}`) : GO_TEST });
      out({ type: "turn_end", message: m, toolResults: [] });
      let steered = await deliverSteering();

      // turn 2: read + edit (optionally behind a dialog)
      if (!S.aborted) {
        out({ type: "turn_start" });
        m = await streamAssistant([
          { type: "thinking", thinking: steered ? `The user steered: "${steered}". I'll take that into account while editing.` : "Tests pass. Now read store.go." },
          { type: "toolCall", name: "read", arguments: { path: "extproc/store.go", offset: 1, limit: 40 } },
          { type: "toolCall", name: "edit", arguments: { path: "extproc/store.go", edits: [{ oldText: "\tflushEvery time.Duration\n", newText: "\tflushEvery    time.Duration\n\tflushOnClose  bool\n" }] } },
        ], "toolUse");
        const [read, edit] = m.content.filter((b) => b.type === "toolCall");
        await runTool(read, READ);
        let allow = true;
        if (lower.includes("confirm")) {
          const r = await dialog("confirm", { title: "Allow edit?", message: "The agent wants to modify extproc/store.go (2 lines).", timeout: 60000 });
          allow = !!r.confirmed;
        } else if (lower.includes("select")) {
          const r = await dialog("select", { title: "Which flush strategy?", options: ["flush on close", "flush every 30s", "both", "neither"] });
          out({ type: "extension_ui_request", id: "n1", method: "notify", message: `chose: ${r.cancelled ? "(cancelled)" : r.value}`, notifyType: "info" });
        } else if (lower.includes("input")) {
          const r = await dialog("input", { title: "Commit message", placeholder: "feat: ..." });
          out({ type: "extension_ui_request", id: "n2", method: "notify", message: `message: ${r.value ?? "(cancelled)"}`, notifyType: "info" });
        }
        if (lower.includes("error")) await runTool(edit, "Error: oldText not found in extproc/store.go (0 matches). The file may have changed since it was read.", { isError: true });
        else if (!allow) await runTool(edit, "Edit blocked by user.", { isError: true });
        else await runTool(edit, "Successfully replaced 1 block in extproc/store.go.", { details: { diff: DIFF, firstChangedLine: 11 } });
        out({ type: "turn_end", message: m, toolResults: [] });
        steered = await deliverSteering();
      }

      // turn 3: final answer
      if (!S.aborted) {
        out({ type: "turn_start" });
        m = await streamAssistant([{ type: "thinking", thinking: "Summarize the change concisely with the code and a table." }, { type: "text", text: FINAL }], "stop");
        out({ type: "turn_end", message: m, toolResults: [] });
      }
    }
  } finally {
    out({ type: "agent_end", messages: [], willRetry: false });
    S.isStreaming = false;
    // follow-ups start a new run once the agent would otherwise stop
    if (S.followUp.length && !S.aborted) {
      const next = S.followUp.shift();
      out({ type: "queue_update", steering: [...S.steering], followUp: [...S.followUp] });
      run(next);
    } else {
      out({ type: "agent_settled" });
    }
  }
}

// ---- command dispatch ---------------------------------------------------------

const commands = [
  { name: "hello", source: "extension", description: "Say hello from the rec extension" },
  { name: "skill:review", source: "skill", description: "Review the current diff against main", location: "user" },
  { name: "fix-tests", source: "prompt", description: "Find and fix failing tests", location: "project" },
];

async function handle(cmd) {
  const { id, type } = cmd;
  switch (type) {
    case "get_state": return respond(id, type, state());
    case "get_messages": return respond(id, type, { messages: S.messages });
    case "get_session_stats": return respond(id, type, stats());
    case "get_commands": return respond(id, type, { commands });
    case "get_available_models": return respond(id, type, { models });
    case "get_available_thinking_levels": return respond(id, type, { levels: S.model.reasoning ? ["off", "minimal", "low", "medium", "high"] : ["off"] });
    case "set_thinking_level": S.thinkingLevel = cmd.level; return respond(id, type);
    case "cycle_thinking_level": {
      if (!S.model.reasoning) return respond(id, type, null);
      const lv = ["off", "minimal", "low", "medium", "high"];
      S.thinkingLevel = lv[(lv.indexOf(S.thinkingLevel) + 1) % lv.length];
      return respond(id, type, { level: S.thinkingLevel });
    }
    case "set_model": {
      const m = models.find((x) => x.provider === cmd.provider && x.id === cmd.modelId);
      if (!m) return respond(id, type, undefined, "model not found");
      S.model = m;
      if (!m.reasoning) S.thinkingLevel = "off";
      return respond(id, type, m);
    }
    case "set_session_name": S.sessionName = cmd.name; respond(id, type); return out({ type: "session_info_changed", name: cmd.name });
    case "abort": S.aborted = true; return respond(id, type);
    case "new_session": S.messages = []; S.tokens = { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 }; S.sessionId = "sess_" + Math.random().toString(36).slice(2, 10); return respond(id, type, { cancelled: false });
    case "get_last_assistant_text": {
      const a = [...S.messages].reverse().find((m) => m.role === "assistant");
      return respond(id, type, { text: a ? a.content.filter((b) => b.type === "text").map((b) => b.text).join("") : null });
    }
    case "compact": {
      out({ type: "compaction_start", reason: "manual" });
      S.isCompacting = true;
      await sleep(900);
      const before = S.tokens.total;
      S.isCompacting = false;
      const summary = "Summary of earlier conversation: added flushOnClose to the extproc Store; tests pass.";
      S.messages = [{ role: "compactionSummary", summary, tokensBefore: before, timestamp: now() }];
      S.tokens = { input: 800, output: 0, cacheRead: 0, cacheWrite: 0, total: 800 };
      out({ type: "compaction_end", reason: "manual", result: { summary, tokensBefore: before }, aborted: false, willRetry: false });
      return respond(id, type, { summary, firstKeptEntryId: "e1", tokensBefore: before, estimatedTokensAfter: 800 });
    }
    case "bash": {
      await sleep(300);
      const output = `fake-pi: would run \`${cmd.command}\`\n` + LS.slice(0, 4).join("\n") + "\n";
      S.messages.push({ role: "bashExecution", command: cmd.command, output, exitCode: 0, cancelled: false, truncated: false, timestamp: now() });
      return respond(id, type, { output, exitCode: 0, cancelled: false, truncated: false });
    }
    case "prompt":
    case "steer":
    case "follow_up": {
      const text = cmd.message ?? "";
      if (type === "prompt" && text.startsWith("/hello")) {
        respond(id, type, { disposition: "handled" });
        return out({ type: "extension_ui_request", id: "hello", method: "notify", message: "hello from the rec extension 👋", notifyType: "info" });
      }
      const mode = type === "steer" ? "steer" : type === "follow_up" ? "followUp" : cmd.streamingBehavior;
      if (S.isStreaming) {
        if (!mode) return respond(id, type, undefined, "Agent is streaming; specify streamingBehavior");
        (mode === "steer" ? S.steering : S.followUp).push(text);
        respond(id, type, { disposition: "queued" });
        return out({ type: "queue_update", steering: [...S.steering], followUp: [...S.followUp] });
      }
      respond(id, type, { disposition: "started" });
      return run(text);
    }
    case "extension_ui_response": {
      const r = S.dialogs.get(cmd.id);
      if (r) { S.dialogs.delete(cmd.id); r(cmd); }
      return;
    }
    default:
      return respond(id, type, undefined, `Unknown command: ${type}`);
  }
}

out({ type: "extension_ui_request", id: "st1", method: "setStatus", statusKey: "rec", statusText: "rec: flint @ :11500" });
const rl = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
rl.on("line", (line) => {
  if (!line.trim()) return;
  let cmd;
  try { cmd = JSON.parse(line); } catch { return out({ type: "response", command: "parse", success: false, error: "bad json" }); }
  handle(cmd).catch((e) => process.stderr.write(String(e?.stack || e) + "\n"));
});
rl.on("close", () => process.exit(0));
