// pi web UI — a browser rendition of pi's terminal interface, driven by the
// RPC event stream from the Go server (/api/events) and RPC commands (/api/cmd).
"use strict";

// ---------------------------------------------------------------- utilities

const $ = (sel) => document.querySelector(sel);
const el = (tag, cls, text) => {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text != null) e.textContent = text;
  return e;
};
const store = {
  get(k, d) { try { const v = localStorage.getItem("piweb." + k); return v == null ? d : JSON.parse(v); } catch { return d; } },
  set(k, v) { try { localStorage.setItem("piweb." + k, JSON.stringify(v)); } catch {} },
};
const fmtNum = (n) => n == null ? "?" : n < 1000 ? String(n) : n < 1e6 ? (n / 1000).toFixed(n < 1e4 ? 1 : 0) + "k" : (n / 1e6).toFixed(1) + "M";
const fmtDur = (ms) => ms < 1000 ? ms + "ms" : ms < 60000 ? (ms / 1000).toFixed(1) + "s" : Math.floor(ms / 60000) + "m" + String(Math.round(ms / 1000) % 60).padStart(2, "0") + "s";
const oneLine = (s, n = 200) => { s = String(s ?? "").replace(/\s+/g, " ").trim(); return s.length > n ? s.slice(0, n - 1) + "…" : s; };
const textOf = (content) => typeof content === "string" ? content
  : Array.isArray(content) ? content.filter((c) => c && c.type === "text").map((c) => c.text).join("") : "";
const resultText = (r) => !r ? "" : typeof r === "string" ? r : Array.isArray(r) ? textOf(r) : textOf(r.content);
const lineCount = (s) => !s ? 0 : s.replace(/\n$/, "").split("\n").length;
const home = (p) => p ? p.replace(/^\/home\/[^/]+/, "~") : p;

async function cmd(type, params = {}) {
  try {
    const r = await fetch("/api/cmd", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ type, ...params }) });
    const j = await r.json();
    if (!j.success) notice("error", `${type}: ${j.error || r.statusText}`);
    return j;
  } catch (e) {
    notice("error", `${type}: ${e.message}`);
    return { success: false, error: e.message };
  }
}

// ---------------------------------------------------------------- state

const S = {
  state: null,          // get_state data
  stats: null,          // get_session_stats data
  info: {},             // {cwd, branch}
  levels: ["off"],
  commands: [],         // get_commands
  streaming: false,
  runStart: 0,
  activity: "",         // spinner label
  retry: null,
  queue: { steering: [], followUp: [] },
  ext: { status: new Map(), widgets: new Map(), title: "" },
  dialogs: [],
  history: [],          // sent prompts, for ↑/↓
  histIdx: -1,
  images: [],
  bell: store.get("bell", false),
  bashRunning: false,
};

const T = {
  root: $("#transcript"),
  entries: 0,
  cur: null,            // streaming assistant: {el, blocks: Map<contentIndex, block>, msg}
  tools: new Map(),     // toolCallId -> tool view
  dirty: new Set(),
};

// ---------------------------------------------------------------- markdown

marked.use({ gfm: true, breaks: false });
DOMPurify.addHook("afterSanitizeAttributes", (n) => {
  if (n.tagName === "A") { n.setAttribute("target", "_blank"); n.setAttribute("rel", "noopener noreferrer"); }
});

function renderMarkdown(target, src) {
  let html;
  try { html = marked.parse(src || ""); } catch { html = ""; }
  target.innerHTML = DOMPurify.sanitize(html, { FORBID_TAGS: ["style", "img", "form", "input"] });
  for (const pre of target.querySelectorAll("pre")) {
    const code = pre.querySelector("code");
    const lang = (code?.className.match(/language-(\S+)/) || [])[1] || "";
    const wrap = el("div", "code");
    const head = el("div", "code-head");
    head.append(el("span", "", lang || " "), copyButton(() => code ? code.textContent : pre.textContent));
    pre.replaceWith(wrap);
    wrap.append(head, pre);
  }
}

function copyButton(get, label = "copy") {
  const b = el("button", "mini-btn", label);
  b.type = "button";
  b.addEventListener("click", async (e) => {
    e.stopPropagation();
    try { await navigator.clipboard.writeText(get()); b.textContent = "copied"; }
    catch { b.textContent = "failed"; }
    setTimeout(() => (b.textContent = label), 1200);
  });
  return b;
}

// ---------------------------------------------------------------- scrolling

const scroller = $("#scroll");
let stick = true;
scroller.addEventListener("scroll", () => {
  stick = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 60;
  if (stick) $("#jump").hidden = true;
});
function afterContent() {
  if (stick) scroller.scrollTop = scroller.scrollHeight;
  else $("#jump").hidden = false;
}
$("#jump").addEventListener("click", () => { stick = true; scroller.scrollTop = scroller.scrollHeight; $("#jump").hidden = true; });

// Coalesce DOM updates for streaming blocks into one pass per frame. Long
// markdown is re-parsed at most every ~80ms.
let frame = 0, lastBig = 0;
function markDirty(block) { T.dirty.add(block); if (!frame) frame = requestAnimationFrame(flush); }
function flush() {
  frame = 0;
  const now = performance.now();
  for (const b of [...T.dirty]) {
    if (b.kind === "text" && b.text.length > 12000 && now - lastBig < 80) continue;
    if (b.kind === "text" && b.text.length > 12000) lastBig = now;
    T.dirty.delete(b);
    b.paint();
  }
  if (T.dirty.size && !frame) frame = requestAnimationFrame(flush);
  afterContent();
}

// ---------------------------------------------------------------- transcript

function append(node) {
  clearEmpty();
  T.root.append(node);
  T.entries++;
  afterContent();
  return node;
}

function clearTranscript() {
  T.root.textContent = "";
  T.entries = 0;
  T.cur = null;
  T.tools.clear();
  T.dirty.clear();
}

function userEntry(msg) {
  const e = el("div", "entry user");
  const text = textOf(msg.content);
  e.textContent = text;
  const imgs = Array.isArray(msg.content) ? msg.content.filter((c) => c.type === "image").length : 0;
  if (imgs) e.append(el("div", "imgs", `[${imgs} image${imgs > 1 ? "s" : ""}]`));
  if (text && !text.startsWith("/")) { S.history.push(text); S.histIdx = -1; }
  return append(e);
}

function notice(level, text, cls = "") {
  return append(el("div", `entry notice ${level} ${cls}`, text));
}

// An assistant message is rendered as its content blocks in order: text as
// markdown, thinking as a collapsed one-liner, tool calls as tool rows.
function assistantEntry() {
  const e = el("div", "entry assistant");
  const actions = el("div", "entry-actions");
  const view = { el: e, blocks: new Map(), msg: null };
  actions.append(copyButton(() => [...view.blocks.values()].filter((b) => b.kind === "text").map((b) => b.text).join("\n\n")));
  e.append(actions);
  append(e);
  return view;
}

function textBlock(parent) {
  const b = { kind: "text", text: "", el: el("div", "md") };
  b.paint = () => renderMarkdown(b.el, b.text);
  parent.append(b.el);
  return b;
}

function thinkingBlock(parent) {
  const b = { kind: "thinking", text: "", live: false, el: el("div", "thinking") };
  const head = el("div", "thinking-head");
  const body = el("div", "thinking-body");
  b.el.append(head, body);
  head.addEventListener("click", () => b.el.classList.toggle("open"));
  b.paint = () => {
    const n = b.text.length;
    head.textContent = `∴ ${b.live ? "thinking…" : "thought"}  ${n ? fmtNum(n) + " chars" : ""}`;
    body.textContent = b.text;
  };
  parent.append(b.el);
  b.paint();
  return b;
}

// ---------------------------------------------------------------- tools

function argSummary(name, a) {
  a = a || {};
  const s = (v) => typeof v === "string" ? v : JSON.stringify(v);
  switch (name) {
    case "bash": {
      const c = String(a.command ?? "");
      const first = c.split("\n")[0];
      return "$ " + first + (c.includes("\n") ? "  …" : "") + (a.timeout ? `   (timeout ${a.timeout}s)` : "");
    }
    case "read": {
      let r = home(a.path ?? a.file_path ?? "");
      if (a.offset || a.limit) r += `:${a.offset ?? 1}${a.limit ? "-" + ((a.offset ?? 1) + a.limit - 1) : ""}`;
      return r;
    }
    case "write": return home(a.path ?? a.file_path ?? "") + (a.content != null ? `  (${lineCount(a.content)} lines)` : "");
    case "edit": {
      const n = Array.isArray(a.edits) ? a.edits.length : 1;
      return home(a.path ?? a.file_path ?? "") + (n > 1 ? `  (${n} edits)` : "");
    }
    case "grep": return `/${a.pattern ?? ""}/` + (a.path ? "  " + home(a.path) : "") + (a.glob ? "  " + a.glob : "");
    case "find": return (a.pattern ?? "") + (a.path ? "  in " + home(a.path) : "");
    case "ls": return home(a.path ?? ".");
    default: return Object.entries(a).map(([k, v]) => `${k}=${oneLine(s(v), 60)}`).join("  ");
  }
}

function toolView(id, name, args, opts = {}) {
  const t = {
    kind: "tool", id, name, args: args || {}, status: "pending", output: "", details: null,
    started: 0, ended: 0, el: el("div", "tool" + (opts.bash ? " bashx" : "")),
  };
  const head = el("div", "tool-head");
  const glyph = el("span", "tool-glyph");
  const nm = el("span", "tool-name", opts.bash ? "!" : name);
  const sum = el("span", "tool-sum");
  const meta = el("span", "tool-meta");
  head.append(glyph, nm, sum, meta);
  const tail = el("div", "tool-tail");
  const body = el("div", "tool-body");
  t.el.append(head, tail, body);
  head.addEventListener("click", () => { t.el.classList.toggle("open"); t.paint(); });
  let bodyKey = "";
  t.paint = () => {
    const st = t.status;
    t.el.classList.toggle("run", st === "running" || st === "pending");
    t.el.classList.toggle("ok", st === "done");
    t.el.classList.toggle("err", st === "error");
    glyph.textContent = st === "done" ? "✓" : st === "error" ? "✗" : spinnerFrame();
    const summary = opts.bash ? "$ " + (t.args.command ?? "") : argSummary(name, t.args);
    sum.textContent = summary;
    sum.title = summary;
    const bits = [];
    const lines = lineCount(t.output);
    if (t.status === "done" || t.status === "error") {
      if (name === "edit" && t.details?.diff) {
        const add = (t.details.diff.match(/^\+/gm) || []).length, del = (t.details.diff.match(/^-/gm) || []).length;
        bits.push(`+${add} -${del}`);
      } else if (lines) bits.push(`${lines} line${lines > 1 ? "s" : ""}`);
      if (t.exitCode != null && t.exitCode !== 0) bits.push(`exit ${t.exitCode}`);
      if (t.started && t.ended - t.started >= 100) bits.push(fmtDur(t.ended - t.started));
    } else if (t.started) {
      bits.push(fmtDur(Date.now() - t.started));
    }
    meta.textContent = bits.join(" · ");
    // collapsed: show the live last line while running, the first lines of an error
    const expanded = t.el.classList.contains("open") || document.body.classList.contains("expand-tools");
    if (st === "error") tail.textContent = t.output.split("\n").filter((l) => l.trim()).slice(0, 3).map((l) => oneLine(l, 160)).join("\n");
    else if (st === "running" && t.output) tail.textContent = t.output.replace(/\n+$/, "").split("\n").pop();
    else tail.textContent = "";
    tail.hidden = !tail.textContent;
    if (expanded) {
      const key = st + "|" + t.output.length + "|" + (t.details ? 1 : 0);
      if (key !== bodyKey) { bodyKey = key; paintToolBody(t, body); }
    }
  };
  T.tools.set(id, t);
  return t;
}

function paintToolBody(t, body) {
  body.textContent = "";
  const a = t.args;
  const pre = (cls, text) => { const p = el("pre", cls); p.textContent = text; body.append(p); return p; };
  if (t.name === "bash" && String(a.command || "").includes("\n")) pre("", a.command);
  if (t.name === "edit") {
    if (t.details?.diff) {
      const p = el("pre", "diff");
      for (const line of t.details.diff.split("\n")) {
        p.append(el("span", line.startsWith("+") ? "add" : line.startsWith("-") ? "del" : "ctx", line + "\n"));
      }
      body.append(p);
    } else {
      const edits = Array.isArray(a.edits) ? a.edits : [{ oldText: a.oldText ?? a.old_string, newText: a.newText ?? a.new_string }];
      const p = el("pre", "diff");
      for (const e of edits) {
        for (const l of String(e.oldText ?? "").split("\n")) p.append(el("span", "del", "- " + l + "\n"));
        for (const l of String(e.newText ?? "").split("\n")) p.append(el("span", "add", "+ " + l + "\n"));
      }
      body.append(p);
    }
  }
  if (t.name === "write" && a.content != null) {
    const content = String(a.content);
    pre("", content.length > 20000 ? content.slice(0, 20000) + "\n… (" + fmtNum(content.length) + " chars)" : content);
  }
  if (!["read", "bash", "edit", "write", "grep", "find", "ls"].includes(t.name) && t.name !== "!") {
    pre("", JSON.stringify(a, null, 2));
  }
  let out = t.output;
  if (out || t.status === "running") {
    const LIMIT = 200000;
    const p = pre("", out.length > LIMIT ? out.slice(0, LIMIT) : out || "…");
    if (out.length > LIMIT) {
      const more = el("div", "more", `… ${fmtNum(out.length - LIMIT)} more chars — show all`);
      more.addEventListener("click", () => { p.textContent = out; more.remove(); });
      body.append(more);
    }
    if (t.status === "running") p.scrollTop = p.scrollHeight;
  }
}

// ---------------------------------------------------------------- spinner

const FRAMES = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏";
let spinI = 0;
const spinnerFrame = () => FRAMES[spinI % FRAMES.length];
setInterval(() => {
  spinI++;
  if (S.streaming || S.bashRunning || S.state?.isCompacting) paintSpinner();
  for (const t of T.tools.values()) if (t.status === "running" || t.status === "pending") t.paint();
}, 80);

function paintSpinner() {
  const sp = $("#spinner");
  const busy = S.streaming || S.bashRunning || S.state?.isCompacting;
  sp.hidden = !busy;
  if (!busy) return;
  const secs = S.runStart ? Math.floor((Date.now() - S.runStart) / 1000) : 0;
  let label = S.activity || "Working…";
  if (S.state?.isCompacting) label = "Compacting…";
  if (S.retry) label = S.retry;
  if (S.dialogs.length) label = "Waiting for your answer…";
  sp.innerHTML = "";
  sp.append(spinnerFrame() + " " + label + " ", el("span", "dim", `${secs}s · esc to interrupt`));
}

// ---------------------------------------------------------------- event handling

let resyncing = null; // {known:Set, buffer:[]} while rebuilding from get_messages

function msgKey(m) { return m ? `${m.role}:${m.timestamp}` : ""; }

function handleEvent(ev) {
  if (resyncing) { resyncing.buffer.push(ev); return; }
  apply(ev);
}

function apply(ev, known) {
  switch (ev.type) {
    case "agent_start":
      setStreaming(true);
      break;
    case "agent_end":
      break;
    case "agent_settled":
      if (S.abortRequested) notice("warn", "interrupted");
      S.abortRequested = false;
      setStreaming(false);
      refreshStats();
      if (S.bell && document.hidden) notify(S.state?.sessionName || "pi", "finished");
      break;
    case "turn_start":
      break;
    case "message_start": {
      const m = ev.message || {};
      if (known?.has(msgKey(m))) break;
      if (m.role === "user") userEntry(m);
      else if (m.role === "assistant") { T.cur = assistantEntry(); T.cur.msg = m; S.activity = "Working…"; }
      break;
    }
    case "message_update":
      onAssistantDelta(ev.assistantMessageEvent || {});
      break;
    case "message_end": {
      const m = ev.message || {};
      if (known?.has(msgKey(m))) break;
      if (m.role === "assistant") {
        if (!T.cur) T.cur = assistantEntry();
        finalizeAssistant(T.cur, m);
        T.cur = null;
        refreshStats();
      } else if (m.role === "toolResult") {
        const t = T.tools.get(m.toolCallId);
        if (t && t.status !== "done" && t.status !== "error") finishTool(t, m.content, m.isError, m.details);
      } else if (m.role !== "user") {
        historyMessage(m);
      }
      break;
    }
    case "tool_execution_start": {
      let t = T.tools.get(ev.toolCallId);
      if (known && t && (t.status === "done" || t.status === "error")) break;
      if (!t) { t = toolView(ev.toolCallId, ev.toolName, ev.args); (T.cur?.el || append(el("div", "entry assistant"))).append(t.el); }
      if (ev.args) t.args = ev.args;
      t.status = "running";
      t.started = Date.now();
      S.activity = `${ev.toolName}…`;
      t.paint();
      break;
    }
    case "tool_execution_update": {
      const t = T.tools.get(ev.toolCallId);
      if (!t || (known && (t.status === "done" || t.status === "error"))) break;
      t.output = resultText(ev.partialResult);
      t.status = "running";
      t.paint();
      afterContent();
      break;
    }
    case "tool_execution_end": {
      const t = T.tools.get(ev.toolCallId);
      if (t && !(known && (t.status === "done" || t.status === "error"))) finishTool(t, ev.result?.content ?? ev.result, ev.isError, ev.result?.details);
      S.activity = "Working…";
      break;
    }
    case "queue_update":
      S.queue = { steering: ev.steering || [], followUp: ev.followUp || [] };
      paintQueue();
      break;
    case "compaction_start":
      S.state && (S.state.isCompacting = true);
      paintSpinner();
      break;
    case "compaction_end":
      S.state && (S.state.isCompacting = false);
      paintSpinner();
      if (ev.aborted) notice("warn", "compaction aborted");
      else if (ev.errorMessage) notice("error", "compaction failed: " + ev.errorMessage);
      else if (ev.result) {
        notice("info", `compacted context (${ev.reason})` + (ev.result.tokensBefore ? ` — was ${fmtNum(ev.result.tokensBefore)} tokens` : ""));
      }
      refreshStats();
      break;
    case "auto_retry_start":
      S.retry = `Retrying (${ev.attempt}/${ev.maxAttempts}) in ${Math.round((ev.delayMs || 0) / 1000)}s — ${oneLine(ev.errorMessage, 80)}`;
      paintSpinner();
      break;
    case "auto_retry_end":
      S.retry = null;
      if (!ev.success && ev.finalError) notice("error", "gave up retrying: " + ev.finalError);
      break;
    case "extension_error":
      notice("error", `extension error (${ev.event}): ${ev.error}`);
      break;
    case "session_info_changed":
      if (S.state) S.state.sessionName = ev.name;
      paintFooter();
      break;
    case "extension_ui_request":
      extensionUI(ev);
      break;
    case "web_dialog_closed":
      closeDialog(ev.id, false);
      break;
  }
}

function onAssistantDelta(e) {
  if (!T.cur) T.cur = assistantEntry(); // joined mid-message; message_end fills the gap
  const v = T.cur;
  const i = e.contentIndex;
  switch (e.type) {
    case "text_start": case "text_delta": {
      let b = v.blocks.get(i);
      if (!b) { b = textBlock(v.el); v.blocks.set(i, b); }
      if (e.delta) { b.text += e.delta; markDirty(b); }
      S.activity = "Writing…";
      break;
    }
    case "thinking_start": case "thinking_delta": {
      let b = v.blocks.get(i);
      if (!b) { b = thinkingBlock(v.el); v.blocks.set(i, b); }
      b.live = true;
      if (e.delta) b.text += e.delta;
      markDirty(b);
      S.activity = "Thinking…";
      break;
    }
    case "thinking_end": {
      const b = v.blocks.get(i);
      if (b) { b.live = false; markDirty(b); }
      break;
    }
    case "toolcall_start": case "toolcall_delta": case "toolcall_end": {
      const block = e.toolCall || e.block;
      let b = v.blocks.get(i);
      if (!b && block?.id) {
        b = T.tools.get(block.id) || toolView(block.id, block.name, block.arguments);
        v.blocks.set(i, b);
        v.el.append(b.el);
      }
      if (b && e.toolCall) { b.args = e.toolCall.arguments || {}; b.paint(); }
      S.activity = `preparing ${block?.name || "tool call"}…`;
      break;
    }
  }
  if (S.streaming) paintSpinner();
}

// Rebuild an assistant view from the authoritative final message, reusing
// existing block views (and tool rows with their execution state).
function finalizeAssistant(v, m) {
  v.msg = m;
  const content = Array.isArray(m.content) ? m.content : [];
  const keep = v.blocks;
  v.blocks = new Map();
  const actions = v.el.querySelector(".entry-actions");
  v.el.textContent = "";
  if (actions) v.el.append(actions);
  content.forEach((c, i) => {
    let b;
    if (c.type === "text") {
      b = keep.get(i)?.kind === "text" ? keep.get(i) : textBlock(v.el);
      v.el.append(b.el);
      if (b.text !== c.text || T.dirty.has(b)) { b.text = c.text; b.paint(); }
    } else if (c.type === "thinking") {
      b = keep.get(i)?.kind === "thinking" ? keep.get(i) : thinkingBlock(v.el);
      v.el.append(b.el);
      b.text = c.thinking || ""; b.live = false; b.paint();
      if (!b.text.trim()) b.el.remove();
    } else if (c.type === "toolCall") {
      b = T.tools.get(c.id) || toolView(c.id, c.name, c.arguments);
      b.args = c.arguments || b.args;
      v.el.append(b.el);
      b.paint();
    }
    if (b) { v.blocks.set(i, b); T.dirty.delete(b); }
  });
  // an aborted message gets one "interrupted" notice at agent_settled instead
  if (m.stopReason === "aborted" && S.streaming) S.abortRequested = true;
  if (m.stopReason === "error") v.el.append(el("div", "notice error", "error: " + (m.errorMessage || "model request failed")));
  else if (m.stopReason === "length") v.el.append(el("div", "notice warn", "stopped: hit max output tokens"));
  if (v.el.childElementCount <= 1) v.el.remove();
  afterContent();
}

function finishTool(t, content, isError, details) {
  t.output = resultText(content);
  t.details = details || t.details;
  t.status = isError ? "error" : "done";
  t.ended = Date.now();
  if (!t.started) t.started = 0;
  t.paint();
  afterContent();
}

// Messages from get_messages, or non-chat roles arriving live.
function historyMessage(m) {
  switch (m.role) {
    case "user": userEntry(m); break;
    case "assistant": finalizeAssistant(assistantEntry(), m); break;
    case "toolResult": {
      const t = T.tools.get(m.toolCallId);
      if (t) finishTool(t, m.content, m.isError, m.details);
      break;
    }
    case "bashExecution": {
      const t = toolView("bash-" + m.timestamp, "!", { command: m.command }, { bash: true });
      append(el("div", "entry")).append(t.el);
      t.exitCode = m.exitCode;
      finishTool(t, m.output + (m.truncated ? "\n… (truncated)" : ""), m.exitCode !== 0 || m.cancelled);
      break;
    }
    case "compactionSummary":
    case "branchSummary": {
      const n = notice("info summary", `${m.role === "compactionSummary" ? "compacted" : "branch"} summary${m.tokensBefore ? ` (was ${fmtNum(m.tokensBefore)} tokens)` : ""} — click to expand`);
      n.style.cursor = "pointer";
      n.addEventListener("click", () => { n.textContent = m.summary || "(empty)"; n.style.cursor = ""; }, { once: true });
      break;
    }
    default: {
      const text = textOf(m.content) || m.summary || "";
      if (text) notice("", `[${m.role}${m.customType ? ":" + m.customType : ""}] ${text}`);
    }
  }
}

// ---------------------------------------------------------------- sync

async function resync() {
  resyncing = { buffer: [] };
  const [st, msgs, info] = await Promise.all([
    cmd("get_state"), cmd("get_messages"), fetch("/api/info").then((r) => r.json()).catch(() => ({})),
  ]);
  S.info = info || {};
  if (st.success) S.state = st.data;
  clearTranscript();
  S.history = [];
  const messages = msgs.success ? msgs.data.messages || [] : [];
  for (const m of messages) historyMessage(m);
  if (!messages.length) showEmpty();
  // Replay events that raced the snapshot, skipping messages it already has.
  const known = new Set(messages.map(msgKey));
  const buffered = resyncing.buffer;
  resyncing = null;
  for (const ev of buffered) apply(ev, known);
  setStreaming(!!S.state?.isStreaming);
  refreshStats();
  loadMeta();
  stick = true;
  afterContent();
}

async function loadMeta() {
  const [lv, cs] = await Promise.all([cmd("get_available_thinking_levels"), cmd("get_commands")]);
  if (lv.success) S.levels = lv.data?.levels || ["off"];
  if (cs.success) S.commands = cs.data?.commands || [];
  paintFooter();
}

let statsTimer = 0;
function refreshStats() {
  clearTimeout(statsTimer);
  statsTimer = setTimeout(async () => {
    const [st, ss] = await Promise.all([cmd("get_state"), cmd("get_session_stats")]);
    if (st.success) S.state = st.data;
    if (ss.success) S.stats = ss.data;
    paintFooter();
  }, 150);
}

function connect() {
  const es = new EventSource("/api/events");
  es.onopen = () => { $("#conn").hidden = true; resync(); };
  es.onmessage = (m) => { try { handleEvent(JSON.parse(m.data)); } catch (e) { console.error(e, m.data); } };
  es.onerror = () => { $("#conn").hidden = false; };
}

// ---------------------------------------------------------------- streaming state

function setStreaming(v) {
  if (v && !S.streaming) S.runStart = Date.now();
  S.streaming = v;
  if (!v) { S.activity = ""; S.retry = null; T.cur = null; }
  $("#editor").classList.toggle("busy", v);
  paintSpinner();
  paintEditorChrome();
  paintTitle();
}

function paintTitle() {
  const name = S.ext.title || S.state?.sessionName || "pi";
  document.title = (S.dialogs.length ? "◆ " : S.streaming ? "● " : "") + name;
}

function notify(title, body) {
  try { if (Notification.permission === "granted") new Notification(title, { body, silent: false }); } catch {}
}

// ---------------------------------------------------------------- footer

function paintFooter() {
  const st = S.state || {}, ss = S.stats || {};
  const cwd = home(S.info.cwd || "");
  $("#f-cwd").textContent = [cwd + (S.info.branch ? ` (${S.info.branch})` : ""), st.sessionName].filter(Boolean).join(" • ");
  const tk = ss.tokens || {};
  const parts = [];
  if (tk.input != null) parts.push(`↑${fmtNum(tk.input)}`, `↓${fmtNum(tk.output)}`);
  if (tk.cacheRead) parts.push(`R${fmtNum(tk.cacheRead)}`);
  if (tk.cacheWrite) parts.push(`W${fmtNum(tk.cacheWrite)}`);
  if (ss.cost) parts.push("$" + ss.cost.toFixed(3));
  const tokens = $("#f-tokens");
  tokens.textContent = parts.join(" ");
  const cu = ss.contextUsage;
  if (cu) {
    const pct = cu.percent == null ? "?" : cu.percent.toFixed(1) + "%";
    const span = el("span", cu.percent > 90 ? "err" : cu.percent > 70 ? "warn" : "", ` ${pct}/${fmtNum(cu.contextWindow)}`);
    if (st.autoCompactionEnabled) span.title = "auto-compaction on";
    tokens.append(span);
  }
  const m = st.model;
  $("#f-model").textContent = m ? (m.name || m.id) : "no model";
  $("#f-model").title = m ? `${m.provider}/${m.id} · change model` : "change model";
  const lvl = st.thinkingLevel || "off";
  $("#f-thinking").textContent = m?.reasoning === false ? "" : "◇ " + lvl;
  $("#f-thinking").style.color = `var(--thinking${lvl[0].toUpperCase() + lvl.slice(1)}, var(--muted))`;
  $("#f-status").textContent = [...S.ext.status.values()].join("  ");
  $("#f-view-thinking").textContent = document.body.classList.contains("show-thinking") ? "thinking: shown" : "thinking: hidden";
  $("#f-view-tools").textContent = document.body.classList.contains("expand-tools") ? "tools: expanded" : "tools: compact";
  $("#f-bell").textContent = S.bell ? "🔔" : "🔕";
  paintEditorChrome();
  paintTitle();
}

function paintEditorChrome() {
  const ed = $("#editor");
  const lvl = S.state?.thinkingLevel || "off";
  const key = "thinking" + lvl[0].toUpperCase() + lvl.slice(1);
  const bash = $("#input").value.startsWith("!");
  ed.classList.toggle("bash", bash);
  ed.style.setProperty("--editorBorder", bash ? "var(--bashMode)" : `var(--${key}, var(--thinkingOff))`);
  $("#editor-label").textContent = bash ? "bash" : "";
  $("#editor-label").hidden = !bash;
  const hint = $("#editor-hint");
  if (S.bashRunning) hint.textContent = "running… esc to cancel";
  else if (S.streaming) hint.textContent = "enter steer · alt+enter follow-up · esc interrupt";
  else hint.textContent = "";
  hint.hidden = !hint.textContent;
  $("#input").placeholder = S.streaming ? "steer the agent while it works…" : "message pi  ·  / commands  ·  ! bash";
}

function paintQueue() {
  const q = $("#queue");
  q.textContent = "";
  for (const s of S.queue.steering) { const d = el("div", "q"); d.append(el("b", "", "↳ steer  "), oneLine(s)); q.append(d); }
  for (const s of S.queue.followUp) { const d = el("div", "q"); d.append(el("b", "", "↳ next   "), oneLine(s)); q.append(d); }
}

// ---------------------------------------------------------------- extension UI

function extensionUI(ev) {
  switch (ev.method) {
    case "select": case "confirm": case "input": case "editor":
      if (!S.dialogs.some((d) => d.id === ev.id)) S.dialogs.push({ ...ev, opened: Date.now() });
      if (S.dialogs.length === 1) showDialog();
      if (S.bell && document.hidden) notify(S.state?.sessionName || "pi", "needs input: " + (ev.title || ""));
      paintTitle();
      break;
    case "notify":
      notice(ev.notifyType === "error" ? "error" : ev.notifyType === "warning" ? "warn" : "info", ev.message || "");
      break;
    case "setStatus":
      if (ev.statusText == null) S.ext.status.delete(ev.statusKey); else S.ext.status.set(ev.statusKey, ev.statusText);
      paintFooter();
      break;
    case "setWidget": {
      if (!ev.widgetLines) S.ext.widgets.delete(ev.widgetKey);
      else S.ext.widgets.set(ev.widgetKey, { lines: ev.widgetLines, below: ev.widgetPlacement === "belowEditor" });
      const above = $("#widgets-above"), below = $("#widgets-below");
      above.textContent = below.textContent = "";
      for (const w of S.ext.widgets.values()) (w.below ? below : above).append(el("div", "", w.lines.join("\n")));
      break;
    }
    case "setTitle":
      S.ext.title = ev.title || "";
      paintTitle();
      break;
    case "set_editor_text": {
      const input = $("#input");
      input.value = ev.text || "";
      autoGrow();
      input.focus();
      break;
    }
  }
}

let dialogTimer = 0;
function showDialog() {
  const d = S.dialogs[0];
  const box = $("#dialog");
  clearInterval(dialogTimer);
  if (!d) { box.hidden = true; box.textContent = ""; $("#editor").hidden = false; $("#input").focus(); paintTitle(); return; }
  $("#editor").hidden = true;
  box.hidden = false;
  box.textContent = "";
  box.append(el("div", "d-title", d.title || "The agent needs input"));
  if (d.message) box.append(el("div", "d-msg", d.message));
  const row = el("div", "d-row");
  const time = el("span", "d-time");
  const answer = (r) => answerDialog(d.id, r);
  let focusEl;

  if (d.method === "select") {
    const opts = el("div", "d-opts");
    let sel = 0;
    const items = (d.options || []).map((o, i) => {
      const it = el("div", "opt");
      it.append(el("span", "k", `${i < 9 ? i + 1 + ". " : ""}${o}`));
      it.addEventListener("click", () => answer({ value: o }));
      it.addEventListener("mousemove", () => { sel = i; mark(); });
      opts.append(it);
      return it;
    });
    const mark = () => items.forEach((it, i) => it.classList.toggle("sel", i === sel));
    mark();
    box.append(opts);
    box.onkeydown = (e) => {
      if (e.key === "ArrowDown" || e.key === "j") { sel = (sel + 1) % items.length; mark(); }
      else if (e.key === "ArrowUp" || e.key === "k") { sel = (sel - 1 + items.length) % items.length; mark(); }
      else if (e.key === "Enter") answer({ value: d.options[sel] });
      else if (/^[1-9]$/.test(e.key) && d.options[+e.key - 1] != null) answer({ value: d.options[+e.key - 1] });
      else return;
      e.preventDefault(); e.stopPropagation(); // keep the key out of the editor
    };
    box.tabIndex = 0;
    focusEl = box;
  } else if (d.method === "confirm") {
    const yes = el("button", "btn primary"); yes.type = "button"; yes.innerHTML = "<u>Y</u>es";
    const no = el("button", "btn"); no.type = "button"; no.innerHTML = "<u>N</u>o";
    yes.onclick = () => answer({ confirmed: true });
    no.onclick = () => answer({ confirmed: false });
    row.append(yes, no);
    box.onkeydown = (e) => {
      if (e.key === "y" || e.key === "Y") answer({ confirmed: true });
      else if (e.key === "n" || e.key === "N") answer({ confirmed: false });
      else return;
      e.preventDefault(); e.stopPropagation();
    };
    box.tabIndex = 0;
    focusEl = yes;
  } else {
    const multi = d.method === "editor";
    const f = el(multi ? "textarea" : "input");
    if (!multi) f.type = "text";
    f.placeholder = d.placeholder || "";
    f.value = d.prefill || "";
    box.append(f);
    const ok = el("button", "btn primary", multi ? "submit (ctrl+enter)" : "submit (enter)"); ok.type = "button";
    ok.onclick = () => answer({ value: f.value });
    row.append(ok);
    f.addEventListener("keydown", (e) => {
      if (e.key === "Enter" && (!multi || e.ctrlKey || e.metaKey)) { e.preventDefault(); e.stopPropagation(); answer({ value: f.value }); }
    });
    focusEl = f;
  }
  const cancel = el("button", "btn", "cancel (esc)"); cancel.type = "button";
  cancel.onclick = () => answer({ cancelled: true });
  row.append(cancel, time);
  box.append(row);
  if (S.dialogs.length > 1) box.append(el("div", "dim", `+${S.dialogs.length - 1} more waiting`));
  if (d.timeout) {
    const tick = () => {
      const left = Math.max(0, d.timeout - (Date.now() - d.opened));
      time.textContent = `auto-answers in ${Math.ceil(left / 1000)}s`;
    };
    tick();
    dialogTimer = setInterval(tick, 500);
  }
  setTimeout(() => focusEl?.focus(), 0);
  stick = true; afterContent();
}

async function answerDialog(id, response) {
  closeDialog(id, true);
  await fetch("/api/extension-ui/response", {
    method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ id, ...response }),
  }).catch((e) => notice("error", "dialog: " + e.message));
}

function closeDialog(id) {
  const i = S.dialogs.findIndex((d) => d.id === id);
  if (i < 0) return;
  S.dialogs.splice(i, 1);
  if (i === 0) showDialog();
  paintTitle();
}

// ---------------------------------------------------------------- editor + commands

const input = $("#input");
function autoGrow() {
  input.style.height = "auto";
  input.style.height = Math.min(input.scrollHeight + 2, window.innerHeight * 0.4) + "px";
}

const BUILTINS = [
  { name: "new", description: "start a new session", run: async () => { const r = await cmd("new_session"); if (r.success && !r.data?.cancelled) resync(); } },
  { name: "compact", description: "compact context [custom instructions]", run: (arg) => cmd("compact", arg ? { customInstructions: arg } : {}) },
  { name: "model", description: "choose a model", run: () => openModelMenu($("#f-model")) },
  { name: "thinking", description: "set thinking level <" + "level>", run: async (arg) => { if (arg) { await cmd("set_thinking_level", { level: arg }); refreshStats(); } else openThinkingMenu($("#f-thinking")); } },
  { name: "name", description: "set session name <name>", run: async (arg) => { if (arg) { await cmd("set_session_name", { name: arg }); refreshStats(); } } },
  { name: "copy", description: "copy the last answer", run: async () => { const r = await cmd("get_last_assistant_text"); if (r.data?.text) navigator.clipboard.writeText(r.data.text).then(() => notice("info", "copied last answer")); } },
  { name: "clear", description: "clear the screen (keeps the session)", run: () => { clearTranscript(); showEmpty(); } },
  { name: "reload", description: "re-render the transcript from the session", run: () => resync() },
  { name: "hotkeys", description: "show keyboard shortcuts", run: () => showHelp() },
];

function commandList() {
  const ext = S.commands.map((c) => ({ name: c.name, description: c.description || "", source: c.source }));
  return [...BUILTINS.map((b) => ({ ...b, source: "built-in" })), ...ext];
}

let popup = { items: [], sel: 0 };
function updatePopup() {
  const v = input.value;
  const box = $("#popup");
  const m = /^\/(\S*)$/.exec(v);
  if (!m || S.dialogs.length) { box.hidden = true; popup.items = []; return; }
  const q = m[1].toLowerCase();
  const items = commandList().filter((c) => c.name.toLowerCase().includes(q))
    .sort((a, b) => (b.name.toLowerCase().startsWith(q) - a.name.toLowerCase().startsWith(q)));
  popup = { items, sel: Math.min(popup.sel, Math.max(0, items.length - 1)) };
  box.textContent = "";
  if (!items.length) { box.hidden = true; return; }
  items.forEach((c, i) => {
    const o = el("div", "opt" + (i === popup.sel ? " sel" : ""));
    o.append(el("span", "k", "/" + c.name), el("span", "d", c.description), el("span", "s", c.source === "built-in" ? "" : c.source));
    o.addEventListener("mousedown", (e) => { e.preventDefault(); popup.sel = i; completePopup(); });
    box.append(o);
  });
  box.hidden = false;
  box.children[popup.sel]?.scrollIntoView({ block: "nearest" });
}
function completePopup() {
  const c = popup.items[popup.sel];
  if (!c) return;
  input.value = "/" + c.name + " ";
  $("#popup").hidden = true;
  popup.items = [];
  autoGrow();
  input.focus();
}

async function submit(mode) {
  const raw = input.value;
  const text = raw.trim();
  if (!text && !S.images.length) return;
  // built-in slash commands
  const sm = /^\/(\S+)\s*([\s\S]*)$/.exec(text);
  if (sm) {
    const b = BUILTINS.find((x) => x.name === sm[1]);
    if (b) { input.value = ""; autoGrow(); paintEditorChrome(); await b.run(sm[2].trim()); return; }
  }
  // !command runs bash directly (not a model turn); output joins the next prompt
  if (text.startsWith("!") && !S.streaming) {
    const command = text.replace(/^!+/, "").trim();
    if (!command) return;
    input.value = ""; autoGrow(); paintEditorChrome();
    const t = toolView("bash-" + Date.now(), "!", { command }, { bash: true });
    append(el("div", "entry")).append(t.el);
    t.status = "running"; t.started = Date.now(); t.paint();
    S.bashRunning = true; paintSpinner(); paintEditorChrome();
    const r = await cmd("bash", { command });
    S.bashRunning = false; paintSpinner(); paintEditorChrome();
    const d = r.data || {};
    t.exitCode = d.exitCode;
    finishTool(t, (d.output ?? r.error ?? "") + (d.truncated ? `\n… (truncated; full output in ${d.fullOutputPath})` : ""), !r.success || d.exitCode !== 0 || d.cancelled);
    return;
  }
  const params = { message: text };
  if (S.images.length) params.images = S.images.map(({ data, mimeType }) => ({ type: "image", data, mimeType }));
  let type = "prompt";
  if (S.streaming) {
    if (text.startsWith("/")) params.streamingBehavior = mode === "followUp" ? "followUp" : "steer"; // extension commands run immediately
    else type = mode === "followUp" ? "follow_up" : "steer";
  }
  input.value = ""; autoGrow(); paintEditorChrome();
  S.images = []; paintAttachments();
  stick = true;
  const r = await cmd(type, params);
  if (!r.success) { input.value = raw; autoGrow(); }
}

input.addEventListener("input", () => { autoGrow(); updatePopup(); paintEditorChrome(); });
input.addEventListener("keydown", (e) => {
  const pop = !$("#popup").hidden && popup.items.length;
  if (pop && (e.key === "ArrowDown" || e.key === "ArrowUp")) {
    popup.sel = (popup.sel + (e.key === "ArrowDown" ? 1 : -1) + popup.items.length) % popup.items.length;
    updatePopup(); e.preventDefault(); return;
  }
  if (pop && e.key === "Tab") { completePopup(); e.preventDefault(); return; }
  if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
    e.preventDefault();
    if (pop) {
      const c = popup.items[popup.sel];
      const typed = input.value.slice(1);
      if (c && c.name !== typed) { completePopup(); return; }
    }
    $("#popup").hidden = true;
    submit(e.altKey ? "followUp" : "steer");
    return;
  }
  if (e.key === "Tab" && e.shiftKey) { e.preventDefault(); cycleThinking(); return; }
  // prompt history
  if (e.key === "ArrowUp" && input.selectionStart === 0 && input.selectionEnd === 0 && S.history.length) {
    if (S.histIdx === -1) { S.draft = input.value; S.histIdx = S.history.length; }
    if (S.histIdx > 0) { S.histIdx--; input.value = S.history[S.histIdx]; autoGrow(); input.setSelectionRange(0, 0); }
    e.preventDefault();
  } else if (e.key === "ArrowDown" && S.histIdx !== -1 && input.selectionStart === input.value.length) {
    S.histIdx++;
    if (S.histIdx >= S.history.length) { S.histIdx = -1; input.value = S.draft || ""; } else input.value = S.history[S.histIdx];
    autoGrow(); e.preventDefault();
  }
});
input.addEventListener("blur", () => setTimeout(() => ($("#popup").hidden = true), 120));

// images: paste or drop, when the model accepts them
function paintAttachments() {
  const a = $("#attachments");
  a.textContent = "";
  S.images.forEach((im, i) => {
    const d = el("div", "att");
    const img = el("img"); img.src = `data:${im.mimeType};base64,${im.data}`;
    const x = el("button", "", "✕"); x.type = "button"; x.onclick = () => { S.images.splice(i, 1); paintAttachments(); };
    d.append(img, im.name || "image", x);
    a.append(d);
  });
}
function addImageFiles(files) {
  const imgs = [...files].filter((f) => f.type.startsWith("image/"));
  if (!imgs.length) return false;
  if (!(S.state?.model?.input || []).includes("image")) { notice("warn", `${S.state?.model?.name || "this model"} doesn't accept images`); return true; }
  for (const f of imgs) {
    const r = new FileReader();
    r.onload = () => { S.images.push({ name: f.name, mimeType: f.type, data: String(r.result).split(",")[1] }); paintAttachments(); };
    r.readAsDataURL(f);
  }
  return true;
}
input.addEventListener("paste", (e) => { if (addImageFiles(e.clipboardData?.files || [])) e.preventDefault(); });
document.addEventListener("dragover", (e) => e.preventDefault());
document.addEventListener("drop", (e) => { e.preventDefault(); addImageFiles(e.dataTransfer?.files || []); });

// ---------------------------------------------------------------- menus (model, thinking)

function openMenu(anchor, items, onPick) {
  const m = $("#menu");
  m.textContent = "";
  let sel = Math.max(0, items.findIndex((i) => i.current));
  const rows = items.map((it, i) => {
    const o = el("div", "opt" + (it.current ? " cur" : ""));
    o.append(el("span", "k", it.label), el("span", "s", it.hint || ""));
    o.addEventListener("click", () => { closeMenu(); onPick(it); });
    o.addEventListener("mousemove", () => { sel = i; mark(); });
    m.append(o);
    return o;
  });
  const mark = () => rows.forEach((r, i) => r.classList.toggle("sel", i === sel));
  mark();
  m.hidden = false;
  const r = anchor.getBoundingClientRect();
  m.style.left = Math.max(8, Math.min(r.left, window.innerWidth - m.offsetWidth - 8)) + "px";
  m.style.top = Math.max(8, r.top - m.offsetHeight - 6) + "px";
  m.onkey = (e) => {
    if (e.key === "ArrowDown") { sel = (sel + 1) % rows.length; mark(); }
    else if (e.key === "ArrowUp") { sel = (sel - 1 + rows.length) % rows.length; mark(); }
    else if (e.key === "Enter") { closeMenu(); onPick(items[sel]); }
    else return false;
    return true;
  };
}
function closeMenu() { $("#menu").hidden = true; $("#menu").onkey = null; input.focus(); }
document.addEventListener("mousedown", (e) => { if (!$("#menu").hidden && !e.target.closest("#menu")) $("#menu").hidden = true; });

async function openModelMenu(anchor) {
  const r = await cmd("get_available_models");
  const models = r.data?.models || [];
  const cur = S.state?.model;
  openMenu(anchor, models.map((m) => ({
    label: m.name || m.id, hint: `${m.provider}/${m.id}${m.reasoning ? " · reasoning" : ""}${(m.input || []).includes("image") ? " · images" : ""}`,
    current: cur && m.id === cur.id && m.provider === cur.provider, m,
  })), async (it) => { await cmd("set_model", { provider: it.m.provider, modelId: it.m.id }); refreshStats(); loadMeta(); });
}
function openThinkingMenu(anchor) {
  const cur = S.state?.thinkingLevel;
  openMenu(anchor, S.levels.map((l) => ({ label: l, current: l === cur })), async (it) => {
    await cmd("set_thinking_level", { level: it.label });
    refreshStats();
  });
}
async function cycleThinking() {
  const r = await cmd("cycle_thinking_level");
  if (r.success && r.data && S.state) { S.state.thinkingLevel = r.data.level; paintFooter(); }
}

$("#f-model").addEventListener("click", (e) => openModelMenu(e.currentTarget));
$("#f-thinking").addEventListener("click", (e) => openThinkingMenu(e.currentTarget));
$("#f-view-thinking").addEventListener("click", () => toggleView("show-thinking"));
$("#f-view-tools").addEventListener("click", () => toggleView("expand-tools"));
$("#f-help").addEventListener("click", () => showHelp());
$("#f-bell").addEventListener("click", async () => {
  if (!S.bell && "Notification" in window && Notification.permission === "default") await Notification.requestPermission();
  S.bell = !S.bell; store.set("bell", S.bell); paintFooter();
});

function toggleView(cls) {
  document.body.classList.toggle(cls);
  store.set(cls, document.body.classList.contains(cls));
  for (const t of T.tools.values()) t.paint();
  paintFooter();
}
for (const cls of ["show-thinking", "expand-tools"]) if (store.get(cls, false)) document.body.classList.add(cls);

function showHelp() {
  notice("info", [
    "enter            send  (while working: steer the agent)",
    "alt+enter        queue a follow-up for when the agent finishes",
    "shift+enter      newline",
    "esc              interrupt the agent / cancel a dialog",
    "shift+tab        cycle thinking level",
    "ctrl+t           show/hide thinking",
    "ctrl+o           expand/collapse all tool output",
    "↑ / ↓            previous prompts (at start of input)",
    "/                commands (built-in, extension, skills, prompts)",
    "!cmd             run a shell command in the sandbox; output goes into the next prompt",
    "click a tool row to expand it; hover an answer to copy it",
  ].join("\n"));
}

// ---------------------------------------------------------------- global keys

document.addEventListener("keydown", (e) => {
  const menu = $("#menu");
  if (!menu.hidden && menu.onkey) {
    if (e.key === "Escape") { closeMenu(); e.preventDefault(); return; }
    if (menu.onkey(e)) { e.preventDefault(); return; }
  }
  if (e.key === "Escape") {
    if (S.dialogs.length) { answerDialog(S.dialogs[0].id, { cancelled: true }); e.preventDefault(); return; }
    if (!$("#popup").hidden) { $("#popup").hidden = true; return; }
    if (S.bashRunning) { cmd("abort_bash"); return; }
    if (S.retry) { cmd("abort_retry"); }
    if (S.streaming) { cmd("abort"); S.abortRequested = true; S.activity = "Interrupting…"; paintSpinner(); e.preventDefault(); }
    return;
  }
  if (e.ctrlKey && !e.metaKey && !e.altKey) {
    if (e.key === "t") { e.preventDefault(); toggleView("show-thinking"); return; }
    if (e.key === "o") { e.preventDefault(); toggleView("expand-tools"); return; }
  }
  // typing anywhere goes to the editor
  if (!S.dialogs.length && document.activeElement !== input && !e.ctrlKey && !e.metaKey && !e.altKey && e.key.length === 1
      && !window.getSelection()?.toString()) {
    input.focus();
  }
});

// ---------------------------------------------------------------- empty state + logo

let logoData = null;
fetch("/api/logo").then((r) => (r.ok ? r.json() : null)).then((d) => { logoData = d; if (T.entries === 0) showEmpty(); }).catch(() => {});

let logoAnim = 0;
function clearEmpty() {
  const e = T.root.querySelector(".empty");
  if (e) { e.remove(); cancelAnimationFrame(logoAnim); logoAnim = 0; }
}

function showEmpty() {
  clearEmpty();
  const wrap = el("div", "empty");
  const logo = el("div", "logo");
  const word = el("div", "wordmark");
  const m = S.state?.model;
  word.append(el("b", "", m?.name || "pi"), document.createElement("br"), el("span", "", S.state?.sessionName || "pi"));
  if (logoData) {
    const shape = logoData.large || logoData.small;
    const pix = el("div", "logo-pixels");
    const cells = [];
    shape.forEach((row, y) => {
      row.forEach((c, x) => {
        const s = el("span", "", c.glyph);
        if (c.glyph !== "⠀") cells.push({ s, owner: c.owner, x, y, w: row.length, h: shape.length });
        pix.append(s);
      });
      pix.append("\n");
    });
    logo.append(pix, word);
    // Same glow as the terminal header in rec-ext/pi-logo.mjs.
    const light = matchMedia("(prefers-color-scheme: light)").matches;
    const low = light ? [206, 196, 220] : [54, 42, 78], high = light ? [70, 48, 108] : [227, 213, 255];
    const t0 = performance.now();
    let last = 0;
    const step = (now) => {
      logoAnim = requestAnimationFrame(step);
      if (now - last < 50) return;
      last = now;
      const phase = ((now - t0) / 9000) % 1;
      for (const c of cells) {
        const off = c.owner / 3 + (c.x / (c.w - 1) - 0.5) * 0.06 + (c.y / (c.h - 1) - 0.5) * 0.03;
        const g = c.owner === 3 ? 0 : (1 + Math.cos((phase - off) * Math.PI * 2)) / 2;
        c.s.style.color = `rgb(${low.map((v, i) => Math.round(v + (high[i] - v) * g)).join(",")})`;
      }
    };
    if (matchMedia("(prefers-reduced-motion: reduce)").matches) step(t0 + 1000); else logoAnim = requestAnimationFrame(step);
  } else {
    logo.append(el("div", "logo-pixels", "π"), word);
  }
  const hints = el("div", "hints");
  hints.innerHTML = '<span class="kbd">/</span> commands · <span class="kbd">!</span> bash · <span class="kbd">esc</span> interrupt · <span class="kbd">ctrl+t</span> thinking · <span class="kbd">?</span> keys';
  wrap.append(logo, hints);
  T.root.append(wrap);
}

// ---------------------------------------------------------------- boot

paintFooter();
connect();
input.focus();
