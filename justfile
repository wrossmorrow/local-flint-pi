
flint_port := "11500"
flint_host := "host.docker.internal"
image_name := "flint-pi-agent"
context_window := "262144"
max_tokens := "262144"
flint_session_logs := "~/.local/share/rec/serve.log"
launch_timeout := "300"

# list targets and exit
default:
    just --list

# containerize for sessions
build: 
    docker build -t {{image_name}} .

# serve flint in the background (no-op if already running)
serve port=flint_port:
    #!/usr/bin/env bash
    set -euo pipefail
    if pgrep -f "rec serve" > /dev/null; then
        echo "rec already running"; exit 0
    fi
    nohup rec serve --port {{port}} > {{flint_session_logs}} 2>&1 &
    pid=$!
    for _ in $(seq 1 {{launch_timeout}}); do
        grep -q "flint is ready" {{flint_session_logs}} && { echo "flint is ready"; exit 0; }
        kill -0 "$pid" 2>/dev/null || { echo "rec serve exited:"; tail -20 {{flint_session_logs}}; exit 1; }
        sleep 1
    done
    echo "timed out waiting for rec"; exit 1

# stop the server
stop:
    rec stop || pkill -f "rec serve"

# tail logs
tail:
    tail -f {{flint_session_logs}}

# run a session
run repo host=flint_host port=flint_port: (serve port)
    docker run --rm -it \
        -v "{{absolute_path(repo)}}":/work \
        -v pi-state:/home/node/.pi \
        -e PI_CODING_AGENT_DIR=/home/node/.pi \
        -e PI_SKIP_VERSION_CHECK=1 \
        -e REC_API_KEY="$( rec auth token )" \
        -e REC_PI_CONFIG='{"baseUrl":"http://{{host}}:{{port}}/v1","contextWindow":{{context_window}},"maxTokens":{{max_tokens}}}' \
        {{image_name}} pi --extension /opt/rec/flint-pi.ts --provider rec --model flint

# clone-and-work session as the GitHub App
run-gh repo host=flint_host port=flint_port: (serve port)
    docker run --rm -it \
        -e GH_TOKEN="$( ./scripts/github-token.sh {{file_name(repo)}} )" \
        -e REPO_SLUG={{repo}} \
        -v pi-state:/home/node/.pi \
        -e PI_CODING_AGENT_DIR=/home/node/.pi \
        -e PI_SKIP_VERSION_CHECK=1 \
        -e REC_API_KEY="$(rec auth token)" \
        -e REC_PI_CONFIG='{"baseUrl":"http://{{host}}:{{port}}/v1","contextWindow":{{context_window}},"maxTokens":{{max_tokens}}}' \
        {{image_name}}
    