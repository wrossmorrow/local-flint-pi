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

GIT_CLONE_DEST=/work/repo
GIT_CLONE_URL="https://github.com/${GITHUB_REPO_NAME}.git"

# git reset of repo, clean if exists and different, checkout default if exists and same, o/w plain clone
if [ -d "${GIT_CLONE_DEST}/.git" ]; then
  echo "${GIT_CLONE_DEST} exists and is a git repo..."
  cd "${GIT_CLONE_DEST}"
  [ "$( git remote get-url origin )" = "${GIT_CLONE_URL}" ] \
    || { echo "different repo in ${GIT_CLONE_DEST}; remove the volume" >&2; exit 1; }
  git fetch --prune origin
  if [ -n "$( git status --porcelain )" ] || [ -n "$( git log --branches --not --remotes --oneline )" ]; then
    echo "unpushed work in ${GIT_CLONE_DEST}; refusing to reset (push it, or remove the volume)" >&2
    exit 1
  fi
  GIT_DEFAULT=$( git rev-parse --abbrev-ref origin/HEAD | sed 's|^origin/||' )
  git checkout -B "${GIT_DEFAULT}" "origin/${GIT_DEFAULT}"
  git clean -fdx
  echo "${GIT_CLONE_DEST} reset to ${GIT_DEFAULT}"
else
  git clone "${GIT_CLONE_URL}" "${GIT_CLONE_DEST}"
fi

AGENT_DIR="${PI_CODING_AGENT_DIR:-$HOME/.pi/agent}"
mkdir -p "${AGENT_DIR}"
cat /opt/agents/*.md > "${AGENT_DIR}/AGENTS.md"

cd /work/repo
export SESSION_NAME=${SESSION_NAME:-${GITHUB_REPO_NAME}}

# run the pi web interface: spawns `pi --mode rpc` and serves the browser UI on
# localhost, which is how the agent is interacted with in this sandbox
exec /usr/local/bin/pi-web
