
flint_port := "11500"
flint_host := "host.docker.internal"
image_base := "ghcr.io/wrossmorrow/flint-pi"
context_window := "262144"
max_tokens := "32768"
flint_session_logs := "~/.local/share/rec/serve.log"
launch_timeout := "300"

# list targets and exit
default:
    just --list

# containerize for sessions, optionally targeting a toolchain stage (agent, python, golang, rust)
build toolchain="agent":
    docker build --target {{ toolchain }} -t {{ image_base }}-{{ toolchain }} .

# containerize all image variants (agent, python, golang, rust)
build-all:
    just build agent
    just build python
    just build golang
    just build rust

# serve flint in the background (no-op if already running)
serve port=flint_port:
    #!/usr/bin/env bash
    set -euo pipefail
    if pgrep -f "rec serve" > /dev/null; then
        echo "rec already running"; exit 0
    fi
    nohup rec serve --port {{ port }} > {{ flint_session_logs }} 2>&1 &
    pid=$!
    for _ in $(seq 1 {{ launch_timeout }}); do
        grep -q "flint is ready" {{ flint_session_logs }} && { echo "flint is ready"; exit 0; }
        kill -0 "$pid" 2>/dev/null || { echo "rec serve exited:"; tail -20 {{ flint_session_logs }}; exit 1; }
        sleep 1
    done
    echo "timed out waiting for rec"; exit 1

# stop the flint server
stop:
    rec stop || pkill -f "rec serve"

# tail flint logs
tail:
    tail -f {{ flint_session_logs }}

# bring up compose setup for a session
up repo toolchain="agent" *flags="": serve
    GITHUB_REPO_NAME={{ repo }} \
    FLINT_HOST={{ flint_host }} \
    FLINT_PORT={{ flint_port }} \
    FLINT_CONTEXT_WINDOW={{ context_window }} \
    FLINT_MAX_TOKENS={{ max_tokens }} \
    REC_API_KEY=$( rec auth token ) \
    AGENT_IMAGE=${AGENT_IMAGE:-{{ image_base }}-{{ toolchain }}} \
    AGENT_TARGET=${AGENT_TARGET:-{{ toolchain }}} \
        docker compose run --rm agent {{ flags }}

# bring down compose setup (not flint, see just stop)
down repo *flags="":
    GITHUB_REPO_NAME={{ repo }} \
    REC_API_KEY=0 \
        docker compose down {{ flags }}
