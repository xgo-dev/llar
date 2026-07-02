#!/usr/bin/env bash
set -euo pipefail

owner=$(printf '%s' "$GITHUB_REPOSITORY_OWNER" | tr '[:upper:]' '[:lower:]')
package=$(printf '%s' "$PACKAGE_NAME" | tr '[:upper:]' '[:lower:]')
encoded_package=$(printf '%s' "$package" | jq -sRr @uri)

if [ -z "${SOURCE_URL:-}" ]; then
  SOURCE_URL="https://github.com/${GITHUB_REPOSITORY}"
fi

echo "image=ghcr.io/${owner}/${package}" >> "$GITHUB_OUTPUT"
echo "source_url=$SOURCE_URL" >> "$GITHUB_OUTPUT"

check_package() {
  local endpoint="$1"
  local output="$RUNNER_TEMP/ghcr-package-response.txt"

  set +e
  gh api --include \
    -H "X-GitHub-Api-Version: 2022-11-28" \
    "$endpoint" >"$output" 2>&1
  local status=$?
  set -e

  if [ "$status" -eq 0 ]; then
    echo "found"
    return 0
  fi
  if grep -Eq "^HTTP/[0-9.]+ 404|HTTP 404" "$output"; then
    echo "missing"
    return 0
  fi

  cat "$output" >&2
  return "$status"
}

user_package=$(check_package "/users/${GITHUB_REPOSITORY_OWNER}/packages/container/${encoded_package}")
org_package=$(check_package "/orgs/${GITHUB_REPOSITORY_OWNER}/packages/container/${encoded_package}")

if [ "$user_package" = "found" ] || [ "$org_package" = "found" ]; then
  echo "exists=true" >> "$GITHUB_OUTPUT"
  echo "Package ${GITHUB_REPOSITORY_OWNER}/${package} already exists; skipping publish."
else
  echo "exists=false" >> "$GITHUB_OUTPUT"
  echo "Package ${GITHUB_REPOSITORY_OWNER}/${package} does not exist; publishing placeholder image."
fi
