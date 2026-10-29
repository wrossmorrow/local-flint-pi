
Wrappers for running [recursive's flint](https://huggingface.co/RecursiveSI/flint) with [pi harness](https://pi.dev/). 

The goal is largely to encapsulate work away from other files (particularly sensitive ones) as well as local copies of a repo. A GitHub app is used to get tokens for interacting with `git` with external permission controls. Flint runs locally, but the agent and its repo copy runs in a container.

The agent base image now ships with Python (with [uv](https://docs.astral.sh/uv/) as the package manager), the Go toolchain, Java (17 and 21), and the [just](https://github.com/casey/just) task runner. Toolchain-specific images are built as derived stages of the same `Dockerfile`:

- `agent` (default): the full agent image
- `python`: adds the Python + uv toolchain on top of the agent image
- `golang`: adds the Go toolchain on top of the agent image
- `rust`: adds the Rust toolchain on top of the agent image
- `java17`: adds the JDK 17 toolchain on top of the agent image
- `java21`: adds the JDK 21 toolchain on top of the agent image

Build a toolchain-specific image with `just build <toolchain>`, or directly via `docker build --target <toolchain> -t <image> .`.

## Web interface

The agent container runs the pi web interface (`web/`): a small Go service that spawns `pi --mode rpc` ([RPC docs](https://pi.dev/docs/latest/rpc)) and serves a browser UI over HTTP + server-sent events at `http://127.0.0.1:8787` (override with `PI_WEB_PORT`).

The UI is styled after pi's terminal interface: a single transcript column, an editor whose border color tracks the thinking level, and pi's footer (cwd/branch, session, token and context usage, model, thinking level). Tool calls render as one-line rows (`bash $ go test ./...`, `read path:1-40`, `edit path  +2 -1`) that expand on click; thinking is collapsed to a one-liner. The page rebuilds itself from `get_messages` when it (re)connects, so reloading mid-run or opening a second tab is safe.

| key | action |
|---|---|
| `enter` / `shift+enter` | send / newline; while the agent works, `enter` steers it |
| `alt+enter` | queue a follow-up for when the agent finishes |
| `esc` | interrupt the agent, cancel a dialog |
| `shift+tab` | cycle thinking level |
| `ctrl+t` / `ctrl+o` | show thinking / expand all tool output |
| `/` | commands: built-ins (`/new`, `/compact`, `/model`, `/thinking`, `/name`, `/copy`, ...) plus extension, skill and prompt commands from `get_commands` |
| `!cmd` | run a shell command directly (pi's `bash` RPC); its output goes into the next prompt |

API (all JSON; writes require `Content-Type: application/json`):

- `GET /api/events` — SSE stream of session events. Each stream starts by replaying open extension dialogs and status/widgets. `message_update` events are slimmed: pi's cumulative `message`/`partial` snapshots are dropped (the UI accumulates deltas and takes the final message from `message_end`).
- `POST /api/cmd` — send one allowlisted RPC command, e.g. `{"type":"prompt","message":"..."}`, `{"type":"steer",...}`, `{"type":"set_thinking_level","level":"high"}`; returns pi's `{success, error?, data?}`.
- `POST /api/extension-ui/response` — answer a dialog (`{"id","value"}` / `{"id","confirmed"}` / `{"id","cancelled":true}`). pi applies dialog timeouts itself.
- `GET /api/state`, `GET /api/stats` — `get_state` / `get_session_stats` passthroughs. `GET /api/info` — cwd and git branch. `GET /api/logo` — the rec header logo pixels (`PI_LOGO_PATH`).
- `POST /api/flush` — force the session uploader to ship the current session's pending content now and report the result: `{success, sessionKey, finalOffset, partCount, hash, shipped}`. Also surfaced as the ⇅ button in the UI footer. Shutting down (SIGTERM/SIGINT or the pi child exiting) performs the same final flush and writes the close marker.

Session files are uploaded to S3 (`sessions/<session-id>/part-<offset>.jsonl.gz`, gzipped JSONL) while the agent runs. The uploader periodically (default every 30s, `PI_WEB_S3_FLUSH_INTERVAL_SECONDS`) ships content the pi child has appended to its session file since the last shipped offset; a pending batch larger than `PI_WEB_S3_MAX_PART_BYTES` (default 5MB) is shipped immediately. Closing a session (flush endpoint, shutdown, or process exit) ships the rest and writes a `sessions/<session-id>/_closed` marker with the final offset, part count, content hash and status (`ok` or `error`).

S3 is optional: when no bucket is configured (`AWS_S3_BUCKET_NAME`, fallback `WEB_AWS_S3_BUCKET_NAME`) the upload flow is inert. Credentials come from `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` (fallbacks `WEB_AWS_ACCESS_KEY_ID` / `WEB_AWS_SECRET_ACCESS_KEY`) and the region from `AWS_REGION` (fallback `WEB_AWS_REGION`, default `us-east-1`).

Requests are only accepted with a loopback `Host` (add others with `PI_WEB_ALLOWED_HOSTS=host1,host2`) and a matching `Origin`, so other websites open in the browser can't drive the agent (DNS rebinding / cross-site POSTs).

To work on the UI without a model, run the server against a scripted stand-in for pi: `just web-dev` (needs Go and Node), then open `http://localhost:8787`. `web/testdata/fake-pi.mjs` streams thinking, markdown, tool calls, dialogs (prompt with "confirm", "select" or "input"), tool errors ("error"), slow runs to interrupt ("abort-me") and long answers ("long").

The service is reached from the host via the `web-gateway` sidecar — an `alpine/socat` TCP forwarder that listens on `127.0.0.1:${PI_WEB_PORT:-8787}:8787` and forwards into the agent's web server over the compose network. `just up <repo>` brings up the full compose stack via `docker compose up`, which includes the gateway; the agent now waits on it via `depends_on: service_started`.

The agent binds the web server on `0.0.0.0` inside the container (via the `PI_WEB_HOST` env var set in compose) rather than loopback, so the forwarder can reach it. The default `PI_WEB_HOST` is loopback, which would only work for a same-container forwarder.

The `pi-web` binary is baked into the base `agent` image during the Docker build (via the `web-build` stage, which builds the Go binary and copies it to `/usr/local/bin/pi-web`), so the default `agent` image, and every toolchain derived from it (`python`, `golang`, `rust`, `java17`, `java21`), include it. Toolchain images built with `just build <toolchain>` therefore run the web interface for that toolchain; the compose `agent` service just selects the build target (`agent`, or the requested toolchain) and the entrypoint execs `pi-web`.

There is no separate `web` image to build: the full agent image is the web-enabled image now.
