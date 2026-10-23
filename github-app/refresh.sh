#!/usr/bin/env bash

set -euo pipefail

: "${GITHUB_REPO_NAME:?}" "${KEY:=/run/secrets/gh_private_key}"
JUST_REPO="${GITHUB_REPO_NAME##*/}"    # strips everything up to the last "/"

trap 'exit 0' TERM INT
while true; do
    KEY="$KEY" /usr/local/bin/github-token.sh "${JUST_REPO}" > /tokens/token.tmp
    mv /tokens/token.tmp /tokens/token
    sleep 2700 & wait $!     # backgrounded so SIGTERM interrupts the sleep
done
