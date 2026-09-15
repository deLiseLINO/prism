#!/bin/bash
set -euo pipefail

fail() {
  echo "FAIL: $1" >&2
  exit 1
}

REPO_ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)
GO_ROOT="${GO_ROOT:-$REPO_ROOT}"
PORT="${PRISM_AGENTS_PORT:-18796}"
RUN_ID="$(date +%Y%m%d-%H%M%S).$$"
RUNDIR=$(mktemp -d /tmp/prism-agentsdrive.XXXXXX)
EVID_FINAL="${PRISM_VERIFY_EVIDENCE_DIR:-$REPO_ROOT/verify/evidence/agents-drive/$RUN_ID}"
EVID_WORK="$RUNDIR/evidence"
mkdir -p "$EVID_WORK" "$RUNDIR/.prism" "$RUNDIR/sandbox/tools" "$RUNDIR/sandbox/home/.local/bin"

cleanup() {
  status=$?
  trap - EXIT INT TERM
  set +e
  if [ -n "${PID:-}" ] && kill -0 "$PID" 2>/dev/null; then
    kill -TERM "$PID" 2>/dev/null
    wait "$PID" 2>/dev/null
  fi
  mkdir -p "$EVID_FINAL"
  cp -R "$EVID_WORK"/. "$EVID_FINAL"/
  rm -rf "$RUNDIR"
  echo "evidence: $EVID_FINAL"
  exit "$status"
}
trap cleanup EXIT INT TERM

for tool in npm bash sh; do
  printf '#!/bin/sh\nexit 0\n' > "$RUNDIR/sandbox/tools/$tool"
  chmod 755 "$RUNDIR/sandbox/tools/$tool"
done
for bin in codex claude grok omp pi opencode opencode2 hermes; do
  printf '#!/bin/sh\nexit 0\n' > "$RUNDIR/sandbox/home/.local/bin/$bin"
  chmod 755 "$RUNDIR/sandbox/home/.local/bin/$bin"
done

cat > "$RUNDIR/.prism/prism.json" <<CONFIG
{
  "version": 1,
  "generation": 0,
  "config": {
    "version": 1,
    "daemon": {"listen": "127.0.0.1:$PORT"},
    "providers": {
      "codex": {"wire": "codex", "models": ["gpt-5.6-luna"]},
      "antigravity": {"wire": "antigravity", "models": ["gemini-3.7-flash"]}
    },
    "combos": {},
    "routes": {},
    "aliases": {}
  }
}
CONFIG

echo "==> building prismd"
(cd "$GO_ROOT" && go build -o "$RUNDIR/prismd" ./cmd/prismd) || fail "go build cmd/prismd"

SANDBOX_PATH="$RUNDIR/sandbox/tools:$RUNDIR/sandbox/home/.local/bin"
PATH="$SANDBOX_PATH" "$RUNDIR/prismd" --listen "127.0.0.1:$PORT" --config "$RUNDIR/.prism/prism.json" --credential-store "$RUNDIR/creds" > "$RUNDIR/out.log" 2>&1 &
PID=$!
for _ in $(seq 1 40); do
  curl -sf "http://127.0.0.1:$PORT/api/v1/health" >/dev/null 2>&1 && break
  sleep 0.25
done
curl -sf "http://127.0.0.1:$PORT/api/v1/health" >/dev/null || fail "daemon health failed"

BASE="http://127.0.0.1:$PORT"
record() {
  local name=$1 expect=$2
  shift 2
  set +e
  curl -s -o "$EVID_WORK/$name.body" -w '%{http_code}' "$@" > "$EVID_WORK/$name.code"
  set -e
  local code
  code=$(cat "$EVID_WORK/$name.code")
  [ "$code" = "$expect" ] || fail "$name: expected HTTP $expect, got $code (body: $(head -c 200 "$EVID_WORK/$name.body" 2>/dev/null))"
}

wait_job_state() {
  local agent=$1 want=$2
  for _ in $(seq 1 100); do
    curl -sf "$BASE/api/v1/agents/$agent/job" > "$EVID_WORK/job-$agent.json" 2>/dev/null || true
    state=$(perl -0777 -ne 'print $1 if /"state"\s*:\s*"([^"]+)"/' "$EVID_WORK/job-$agent.json" 2>/dev/null || true)
    [ "$state" = "$want" ] && return 0
    case "$state" in failed|unsupported|interrupted) fail "job for $agent ended in $state: $(cat "$EVID_WORK/job-$agent.json")";; esac
    sleep 0.1
  done
  fail "job for $agent never reached $want (last: $(cat "$EVID_WORK/job-$agent.json" 2>/dev/null))"
}

echo "==> agents status"
record agents-list 200 "$BASE/api/v1/agents"
grep -q '"id":"codex"' "$EVID_WORK/agents-list.body" || fail "codex missing from agents list"
grep -q '"id":"hermes"' "$EVID_WORK/agents-list.body" || fail "hermes missing from agents list"
grep -q '"source":"script"' "$EVID_WORK/agents-list.body" || fail "sandbox binaries should classify as script installs"
record agents-get-grok 200 "$BASE/api/v1/agents/grok"
record agents-get-unknown 404 "$BASE/api/v1/agents/frobnicate"

echo "==> install job lifecycle"
record agents-install-codex 202 -X POST "$BASE/api/v1/agents/codex/install"
grep -q '"command":"npm install -g @openai/codex"' "$EVID_WORK/agents-install-codex.body" || fail "install should resolve the npm plan: $(cat "$EVID_WORK/agents-install-codex.body")"
wait_job_state codex succeeded
record agents-status-codex 200 "$BASE/api/v1/agents/codex"
grep -q '"installed":true' "$EVID_WORK/agents-status-codex.body" || fail "codex should report installed"

echo "==> update job"
record agents-update-grok 202 -X POST "$BASE/api/v1/agents/grok/update"
wait_job_state grok succeeded

echo "==> negative surface"
record agents-job-pi 200 "$BASE/api/v1/agents/pi/job"
grep -q '"state":"idle"' "$EVID_WORK/agents-job-pi.body" || fail "never-driven agent should report idle"
record agents-update-unknown 404 -X POST "$BASE/api/v1/agents/frobnicate/update"
record agents-wrong-method 405 -X PUT "$BASE/api/v1/agents/grok"

echo "PASS: agents install/update drive"
