#!/usr/bin/env bash

set -euo pipefail

set -a && source ./.env && set +a

GITHUB_APP_ID="${GITHUB_APP_ID:?}"
GITHUB_APP_INSTALLATION_ID="${GITHUB_APP_INSTALLATION_ID:?}"
GITHUB_APP_PRIVATE_KEY="${GITHUB_APP_PRIVATE_KEY:?}"

GITHUB_REPO_NAME="${1:-}"

b64() { 
    openssl base64 -A | tr '+/' '-_' | tr -d '='; 
}

now=$( date +%s )
header=$( printf '{"alg":"RS256","typ":"JWT"}' | b64 )
payload=$( printf '{"iat":%d,"exp":%d,"iss":"%s"}' $((now-60)) $((now+540)) "${GITHUB_APP_ID}" | b64 )
sig=$( printf '%s.%s' "$header" "$payload" | openssl dgst -sha256 -sign "${GITHUB_APP_PRIVATE_KEY}" -binary | b64 )

GITHUB_REQUEST_BODY='{}'
[ -n "${GITHUB_REPO_NAME}" ] && GITHUB_REQUEST_BODY=$( printf '{"repositories":["%s"]}' "${GITHUB_REPO_NAME}" )

curl -fsS -X POST \
  -H "Authorization: Bearer $header.$payload.$sig" \
  -H "Accept: application/vnd.github+json" \
  -d "${GITHUB_REQUEST_BODY}" \
  "https://api.github.com/app/installations/${GITHUB_APP_INSTALLATION_ID}/access_tokens" \
  | jq -r .token
