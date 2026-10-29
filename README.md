
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

The agent container runs the [pi web interface](https://pi.dev/docs/latest/rpc) (`web/`): a small Go service that spawns `pi --mode rpc` and serves a browser UI over HTTP + server-sent events. The UI is served on startup at `http://127.0.0.1:8787` (override with `PI_WEB_PORT`) and lets you send prompts, watch streamed responses, approve/answer extension dialogs, and abort runs.

- `POST /api/prompt` — send a prompt (body: `{"message": "..."}`), returns the prompt disposition.
- `GET /api/events` — server-sent events stream of session events and extension UI requests.
- `POST /api/extension-ui/response` — answer a pending extension dialog (`{"id": "...", "value": "..."}` / `{"id": "...", "confirmed": true}` / `{"id": "...", "cancelled": true}`).
- `GET /api/state`, `GET /api/stats`, `POST /api/abort`, `POST /api/clear_queue` — session introspection and control.

The service is reached from the host via the compose port mapping (`127.0.0.1:8787:8787`); `just up <repo>` runs it with `--service-ports`.
