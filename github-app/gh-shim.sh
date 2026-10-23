#!/usr/bin/env bash
# Use the sidecar's current token when present; otherwise keep whatever GH_TOKEN is set.
if [ -r /tokens/token ]; then
    GH_TOKEN="$(cat /tokens/token)"
    export GH_TOKEN
fi
exec /usr/local/lib/gh/gh "$@"
