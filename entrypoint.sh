#!/usr/bin/env bash

set -euo pipefail

# : "${GH_TOKEN:?}" "${REPO_SLUG:?}"   # e.g. wrossmorrow/simple-echo-server
: "${GITHUB_REPO_NAME:?}"

# git reads the token from the env at use time; it's never written to .git/config
git config --global credential.helper \
  '!f() { echo username=x-access-token; echo "password=$(cat /tokens/token)"; }; f'
git config --global user.name  "${GIT_NAME:-flint-agent[bot]}"
git config --global user.email "${GIT_EMAIL:-flint-agent[bot]@users.noreply.github.com}"

git clone "https://github.com/${GITHUB_REPO_NAME}.git" /work/repo

AGENT_DIR="${PI_CODING_AGENT_DIR:-$HOME/.pi/agent}"
mkdir -p "${AGENT_DIR}"
cat /opt/agents/*.md > "${AGENT_DIR}/AGENTS.md"

cd /work/repo
exec pi --extension /opt/rec/flint-pi.ts --provider rec --model flint
