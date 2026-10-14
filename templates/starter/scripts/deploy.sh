#!/usr/bin/env sh
# Uploads a built binary to Sideyard and waits for the release to go live.
# Fails (non-zero exit) if the release fails its health check.
# Needs SIDEYARD_URL, SIDEYARD_PROJECT and SIDEYARD_TOKEN.
set -eu

bin="${1:-app}"
: "${SIDEYARD_URL:?set SIDEYARD_URL, e.g. https://sideyard.example.com}"
: "${SIDEYARD_PROJECT:?set SIDEYARD_PROJECT to the Sideyard project name}"
: "${SIDEYARD_TOKEN:?set SIDEYARD_TOKEN to the project deploy token}"

commit="$(git rev-parse HEAD 2>/dev/null || true)"
message="$(git log -1 --pretty=%s 2>/dev/null || true)"

curl --fail-with-body -sS \
  -H "Authorization: Bearer $SIDEYARD_TOKEN" \
  -F "binary=@$bin" \
  --form-string "commit=$commit" \
  --form-string "message=$message" \
  "$SIDEYARD_URL/api/projects/$SIDEYARD_PROJECT/deploys"
echo
