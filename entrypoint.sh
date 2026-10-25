#!/usr/bin/env bash

set -euo pipefail

: "${GITHUB_REPO_NAME:?}" "${GITHUB_APP_USER:?}" "${GITHUB_APP_USER_ID:?}"

DEFAULT_GIT_NAME="${GITHUB_APP_USER}[bot]"
DEFAULT_GIT_EMAIL="${GITHUB_APP_USER_ID}+${GITHUB_APP_USER}[bot]@users.noreply.github.com"

# git reads the token from the sidecar at use time; it's never written to .git/config
git config --global credential.helper \
  '!f() { echo username=x-access-token; echo "password=$( cat /tokens/token )"; }; f'
git config --global user.name  "${GIT_NAME:-${DEFAULT_GIT_NAME}}"
git config --global user.email "${GIT_EMAIL:-${DEFAULT_GIT_EMAIL}}"

git clone "https://github.com/${GITHUB_REPO_NAME}.git" /work/repo

AGENT_DIR="${PI_CODING_AGENT_DIR:-$HOME/.pi/agent}"
mkdir -p "${AGENT_DIR}"
cat /opt/agents/*.md > "${AGENT_DIR}/AGENTS.md"

cd /work/repo
exec pi --extension /opt/rec/flint-pi.ts --provider rec --model flint
