#!/bin/bash
set -euo pipefail

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  exit 1
}

script_dir=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
repo=$(CDPATH= cd -- "$script_dir/../.." && pwd -P)
PORT="${PRISM_PORT:-18787}"

assert_prerequisites() {
  [ -f "$repo/go.mod" ] || fail "missing root go.mod"
  module=$(sed -n '/^module[[:space:]]/ { s/^module[[:space:]]*//; p; q; }' "$repo/go.mod")
  [ "$module" = "prism" ] || fail "unexpected go.mod module: $module"
  [ -f "$repo/package.json" ] || fail "missing root package.json"
  command -v node >/dev/null 2>&1 || fail "node is required to inspect package.json"
  node -e 'const fs = require("fs"); const packageJson = JSON.parse(fs.readFileSync(process.argv[1], "utf8")); const workspaces = packageJson.workspaces; if (!Array.isArray(workspaces) || !workspaces.includes("packages/contracts") || !workspaces.includes("apps/desktop")) process.exit(1)' "$repo/package.json" || fail "root package.json must workspace packages/contracts and apps/desktop"
}

run_step() {
  name=$1
  shift
  printf '==> %s\n' "$name"
  "$@"
}

run_local_steps() {
  run_step "go test ./..." go test ./...
  run_step "go test -race ./..." go test -race ./...
  run_step "go vet ./..." go vet ./...
  run_step "npm run typecheck" npm run typecheck
  run_step "vitest desktop" node_modules/.bin/vitest run --root "$repo" --dir apps/desktop/test
  run_step "prismd-smoke.sh" bash "$repo/verify/scripts/prismd-smoke.sh"
  run_step "prismctl-proof.sh" bash "$repo/verify/scripts/prismctl-proof.sh"
  run_step "desktop-controls.sh" bash "$repo/verify/scripts/desktop-controls.sh"
  run_step "api-sweep.sh" bash "$repo/verify/scripts/api-sweep.sh"
  run_step "integration-drive.sh" env PRISM_VERIFY_LIVE=0 bash "$repo/verify/scripts/integration-drive.sh"
}

live_accounts_available() {
  [ -f "$HOME/.prism/prism.json" ] || return 1
  [ -d "$HOME/.prism/credentials" ] || return 1
  return 0
}

live_client_prerequisites_available() {
  command -v grok >/dev/null 2>&1 || return 1
  command -v omp >/dev/null 2>&1 || return 1
  [ -x "$repo/node_modules/.bin/electron" ] || return 1
  [ -n "${HOME:-}" ] || return 1
  live_accounts_available || return 1
  return 0
}

live_shared_instance_running() {
  curl -sf --max-time 3 -o /dev/null "http://127.0.0.1:$PORT/api/v1/health" 2>/dev/null
}

run_live_steps() {
  run_step "auth-proof.sh" bash "$repo/verify/scripts/auth-proof.sh"
  run_step "quota-proof.sh" bash "$repo/verify/scripts/quota-proof.sh"
  run_step "integration-drive.sh (live)" env PRISM_VERIFY_LIVE=1 bash "$repo/verify/scripts/integration-drive.sh"
  run_step "live-matrix.sh" bash "$repo/verify/scripts/live-matrix.sh"
}

[ "$#" -eq 1 ] || { printf 'usage: %s local|live\n' "$0" >&2; exit 2; }
mode=$1
case "$mode" in
  local|live) ;;
  *) printf 'unknown mode: %s\n' "$mode" >&2; exit 2 ;;
esac

if live_shared_instance_running; then
  printf 'FAIL: a daemon is already bound to port %s; stop it before verification (the skill refuses to double-drive a shared instance)\n' "$PORT" >&2
  exit 1
fi

assert_prerequisites
cd "$repo"
run_local_steps
if [ "$mode" = "live" ]; then
  if ! live_client_prerequisites_available; then
    printf '%s\n' "VERIFIED_UNREACHABLE"
    exit 3
  fi
  run_live_steps
  printf '%s\n' "VERIFIED live"
else
  printf '%s\n' "VERIFIED local"
fi
