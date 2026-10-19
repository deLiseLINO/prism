#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)
GO_ROOT="${GO_ROOT:-$REPO_ROOT}"
PORT="${PRISM_PORT:-18897}"
CDP_PORT="${PRISM_CDP_PORT:-19332}"
HEADLESS="${PRISM_HEADLESS:-1}"
source "$REPO_ROOT/verify/scripts/owned-runtime.sh"
verify_init integration-recovery-drive "$PORT" "$CDP_PORT"

node "$REPO_ROOT/verify/scripts/integration-recovery-drive.mjs" seed "$RUNDIR" "$PORT"

echo "==> building prism and desktop"
(cd "$GO_ROOT" && go build -o "$RUNDIR/prism" ./cmd/prism) > "$EVID_WORK/build-go.log" 2>&1
npm run build --prefix "$REPO_ROOT" > "$EVID_WORK/build-desktop.log" 2>&1

echo "==> launching the isolated daemon and Electron renderer"
verify_start_electron
verify_attest_daemon

node "$REPO_ROOT/verify/scripts/integration-recovery-drive.mjs" drive "$RUNDIR" "$PORT" "$WS"

verify_quit_app
verify_attest_daemon
verify_stop_daemon
verify_finish 0
printf 'recovery verification passed; evidence: %s\n' "$EVID_FINAL"
