
flint_port := "11500"
flint_host := "host.docker.internal"
image_base := "ghcr.io/wrossmorrow/flint-pi"
context_window := "262144"
max_tokens := "32768"
flint_session_logs := "~/.local/share/rec/serve.log"
launch_timeout := "300"

inkling_port := env("INKLING_PORT", "11501")
inkling_provider := env("INKLING_PROVIDER", "llama-server")
inkling_llama_server := env("INKLING_LLAMA_SERVER", "llama-server")
inkling_api_key := env("INKLING_API_KEY", "local")
inkling_context_window := env("INKLING_CONTEXT_WINDOW", "131072")
inkling_max_tokens := env("INKLING_MAX_TOKENS", "32768")
inkling_launch_timeout := env("INKLING_LAUNCH_TIMEOUT", "600")
inkling_model_id := "thinkingmachines/Inkling-2-Mini"
inkling_hf_repo := env("INKLING_HF_REPO", "unsloth/Inkling-2-Mini-GGUF:UD-Q4_K_XL")
inkling_session_logs := "~/.local/share/inkling/serve.log"

# list targets and exit
default:
    just --list

# containerize for sessions, optionally targeting a toolchain stage (agent, python, golang, rust, java17, java21)
build toolchain="agent":
    docker build --build-arg BUILD_SHA=$(git rev-parse --short HEAD) --target {{ toolchain }} -t {{ image_base }}-{{ toolchain }} .

# containerize all image variants (agent, python, golang, rust, java17, java21)
build-all:
    just build agent
    just build python
    just build golang
    just build rust
    just build java17
    just build java21

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
    rec stop 2>/dev/null || pkill -f "rec serve" || true

# tail flint logs
tail:
    tail -f {{ flint_session_logs }}

# pre-fetch the inkling GGUF weights in the foreground (avoids first-run serve timeout)
fetch-inkling hf_repo=inkling_hf_repo:
    #!/usr/bin/env bash
    set -euo pipefail
    repo="{{ hf_repo }}"
    quant=""
    if [[ "$repo" == *:* ]]; then
        quant="${repo#*:}"
        repo="${repo%%:*}"
    fi
    if command -v hf >/dev/null 2>&1; then
        if [ -n "$quant" ]; then
            hf download "$repo" --include "*${quant}*" --include "*mmproj*"
        else
            hf download "$repo"
        fi
    else
        echo "'hf' CLI not found; download interactively with:" >&2
        echo "  {{ inkling_llama_server }} -hf \"{{ hf_repo }}\" --port {{ inkling_port }} --host 127.0.0.1" >&2
        exit 1
    fi

# serve inkling 2 mini in the background via llama-server (no-op if already running)
serve-inkling port=inkling_port ctx=inkling_context_window hf_repo=inkling_hf_repo:
    #!/usr/bin/env bash
    set -euo pipefail
    if curl -fsS -H "Authorization: Bearer {{ inkling_api_key }}" "http://127.0.0.1:{{ port }}/health" > /dev/null 2>&1; then
        echo "inkling already running on :{{ port }}"; exit 0
    fi
    pid=$(pgrep -f "llama-server.*--alias {{ inkling_model_id }}" | head -1 || true)
    if [ -n "$pid" ]; then
        echo "inkling already starting (pid $pid); waiting for :{{ port }}..."
    else
        mkdir -p "$(dirname {{ inkling_session_logs }})"
        nohup {{ inkling_llama_server }} \
            -hf "{{ hf_repo }}" \
            --alias "{{ inkling_model_id }}" \
            --host 127.0.0.1 \
            --port "{{ port }}" \
            --api-key "{{ inkling_api_key }}" \
            --parallel 1 \
            --ctx-size "{{ ctx }}" \
            --jinja \
            --temp 1.0 \
            --top-p 1.0 \
            --top-k 0 \
            --min-p 0.0 \
            > {{ inkling_session_logs }} 2>&1 &
        pid=$!
    fi
    for _ in $(seq 1 {{ inkling_launch_timeout }}); do
        curl -fsS -H "Authorization: Bearer {{ inkling_api_key }}" "http://127.0.0.1:{{ port }}/health" > /dev/null 2>&1 && { echo "inkling is ready"; exit 0; }
        kill -0 "$pid" 2>/dev/null || { echo "llama-server exited:"; tail -20 {{ inkling_session_logs }}; exit 1; }
        sleep 1
    done
    echo "timed out waiting for inkling (if downloading weights for the first time, watch 'just tail-inkling' or pre-fetch with 'just fetch-inkling')"; exit 1

# stop the inkling server
stop-inkling:
    pkill -f "llama-server.*{{ inkling_model_id }}" || true

# tail inkling logs
tail-inkling:
    tail -f {{ inkling_session_logs }}

# bring up compose setup for a session (model: flint or inkling)
up repo toolchain="agent" model="flint" *flags="":
    #!/usr/bin/env bash
    set -euo pipefail
    case "{{ model }}" in
        flint|rec/flint)
            just stop-inkling
            just serve
            export PI_PROVIDER="rec"
            export PI_MODEL="flint"
            export REC_API_KEY="$( rec auth token )"
            ;;
        inkling|thinkingmachines/Inkling-2-Mini)
            just stop
            just serve-inkling
            export PI_PROVIDER="{{ inkling_provider }}"
            export PI_MODEL="{{ inkling_model_id }}"
            export REC_API_KEY=""
            ;;
        *)
            echo "unknown model '{{ model }}' (expected flint, rec/flint, inkling, or {{ inkling_model_id }})" >&2
            exit 1
            ;;
    esac
    GITHUB_REPO_NAME={{ repo }} \
    FLINT_HOST={{ flint_host }} \
    FLINT_PORT={{ flint_port }} \
    FLINT_CONTEXT_WINDOW={{ context_window }} \
    FLINT_MAX_TOKENS={{ max_tokens }} \
    INKLING_PROVIDER={{ inkling_provider }} \
    INKLING_API_KEY={{ inkling_api_key }} \
    INKLING_CONTEXT_WINDOW={{ inkling_context_window }} \
    INKLING_MAX_TOKENS={{ inkling_max_tokens }} \
    AGENT_IMAGE=${AGENT_IMAGE:-{{ image_base }}-{{ toolchain }}} \
    AGENT_TARGET=${AGENT_TARGET:-{{ toolchain }}} \
        docker compose up {{ flags }}

# bring down compose setup (not flint, see just stop)
down repo *flags="":
    GITHUB_REPO_NAME={{ repo }} \
    REC_API_KEY=0 \
        docker compose down {{ flags }}

query-usage-table query host="127.0.0.1" port="9091":
    curl -s -X POST http://{{host}}:{{port}}/query \
        -H 'content-type: application/json'\
        -H 'accept: application/json' \
        -d '{"query": "{{query}}"}' | jq .

flush-usage-table host="127.0.0.1" port="9091":
    curl -s -X POST http://{{host}}:{{port}}/flush \
        -H 'content-type: application/json'\
        -H 'accept: application/json' \
        | jq .

# run the web UI against a scripted fake pi (no model or container needed)
web-dev port="8787":
    cd web && PI_BIN=node PI_ARGS=testdata/fake-pi.mjs PI_LOGO_PATH=../rec-ext/logo-pixels.json PI_WEB_PORT={{ port }} go run .


# commit and push whatever the running agent has, without involving the model
rescue:
    #!/usr/bin/env bash
    set -euo pipefail
    c=$(docker ps -q --filter label=com.docker.compose.service=agent | head -1)
    [ -n "$c" ] || { echo "no running agent container"; exit 1; }
    docker cp "$c":/work/repo "./rescues/$(date +%Y%m%d-%H%M%S)"
    docker exec "$c" bash -c 'cd /work/repo && git add -A && git commit -m "chore: wip rescue at $(date +%Y%m%d-%H%M%S)" && git push -u origin HEAD'

# everything reviewers said on a PR: conversation, reviews, and inline line comments
pr-feedback pr:
    gh pr view {{pr}} --comments
    gh api "repos/{owner}/{repo}/pulls/{{pr}}/reviews" --jq '.[] | "[\(.state)] \(.user.login): \(.body)"'
    gh api "repos/{owner}/{repo}/pulls/{{pr}}/comments" --jq '.[] | "\(.path):\(.line // .original_line) \(.user.login): \(.body)"'
