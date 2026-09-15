#!/bin/bash
# Headless prismd smoke: empty config boots, health answers, SIGTERM is graceful.
set -u
fail() { echo "FAIL: $1" >&2; exit 1; }

REPO_ROOT="${REPO_ROOT:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)}"
GO_ROOT="${GO_ROOT:-$REPO_ROOT}"
RUNDIR=$(mktemp -d /tmp/prism-smoke.XXXXXX)
PORT="${PRISM_SMOKE_PORT:-18789}"
trap '[ -n "${PID:-}" ] && kill -TERM "$PID" 2>/dev/null; rm -rf "$RUNDIR"' EXIT

( cd "$GO_ROOT" && go build -o "$RUNDIR/prismd" ./cmd/prismd ) || fail "go build cmd/prismd"

"$RUNDIR/prismd" --listen "127.0.0.1:$PORT" --config "$RUNDIR/prism.json" --credential-store "$RUNDIR/creds" > "$RUNDIR/out.log" 2>&1 &
PID=$!
for _ in $(seq 1 40); do
  curl -sf "http://127.0.0.1:$PORT/api/v1/health" >/dev/null 2>&1 && break
  sleep 0.25
done

HEALTH=$(curl -s "http://127.0.0.1:$PORT/api/v1/health")
CODE=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/api/v1/health")
echo "$HEALTH" | grep -q '"status":"ok"' || fail "health body: '$HEALTH'"
[ "$CODE" = "200" ] || fail "health code: $CODE"

kill -TERM "$PID"
wait "$PID" || fail "daemon exited nonzero on SIGTERM"
grep -q 'shutdown complete' "$RUNDIR/out.log" || fail "no graceful shutdown log line"
test -f "$RUNDIR/creds/secret.bin" || fail "credential store dir was not created"

echo "prismd smoke OK (health 200, SIGTERM graceful, secret persisted)"
