#!/bin/bash
set -euo pipefail

fail() {
  echo "FAIL: $1" >&2
  exit 1
}

REPO_ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd -P)
GO_ROOT="${GO_ROOT:-$REPO_ROOT}"
PY3="${PYTHON:-}"
if [ -z "$PY3" ]; then
  { for cand in "$(command -v python3 2>/dev/null)" /usr/bin/python3; do
      [ -n "$cand" ] || continue
      ( "$cand" -c 'import json' ) >/dev/null 2>&1 &
      if wait $!; then PY3="$cand"; break; fi
    done; } 2>/dev/null
fi
[ -n "$PY3" ] || fail "no working python3 found"
PORT="${PRISM_PORT:-${PRISMCTL_PROOF_PORT:-18792}}"
RUNDIR=$(mktemp -d /tmp/prism-apisweep.XXXXXX)
RUN_ID="$(date +%Y%m%d-%H%M%S).$$"
EVID_FINAL="${PRISM_VERIFY_EVIDENCE_DIR:-$REPO_ROOT/verify/evidence/api-sweep/$RUN_ID}"
EVID_WORK="$RUNDIR/evidence"
mkdir -p "$EVID_WORK" "$RUNDIR/.prism"

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

"$RUNDIR/prismd" --listen "127.0.0.1:$PORT" --config "$RUNDIR/.prism/prism.json" --credential-store "$RUNDIR/creds" > "$RUNDIR/out.log" 2>&1 &
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

echo "==> management read endpoints"
record health 200 "$BASE/api/v1/health"
record models 200 "$BASE/api/v1/models"
record providers 200 "$BASE/api/v1/providers"
record accounts 200 "$BASE/api/v1/accounts"
record combos 200 "$BASE/api/v1/combos"
record routes 200 "$BASE/api/v1/routes"
record usage 200 "$BASE/api/v1/usage"
record integrations 200 "$BASE/api/v1/integrations"
record integrations-grok 200 "$BASE/api/v1/integrations/grok"
record integrations-omp 200 "$BASE/api/v1/integrations/omp"

echo "==> management negative surface"
record unknown-path 404 "$BASE/api/v1/frobnicate"
record models-wrong-method 405 -X POST "$BASE/api/v1/models"
record malformed-json 400 -X POST -H 'Content-Type: application/json' -d '{broken' "$BASE/api/v1/providers"

echo "==> provider mutations with CAS"
GEN=$("$PY3" -c 'import json,sys; print(json.load(open(sys.argv[1]))["generation"])' "$EVID_WORK/providers.body")
record provider-create 200 -X POST -H 'Content-Type: application/json' \
  -d "{\"id\":\"probe\",\"wire\":\"responses\",\"baseURL\":\"http://127.0.0.1:9/v1\",\"models\":[\"probe-model\"],\"expectedGeneration\":$GEN}" \
  "$BASE/api/v1/providers"
GEN=$("$PY3" -c 'import json,sys; print(json.load(open(sys.argv[1]))["generation"])' "$EVID_WORK/provider-create.body")
record provider-cas-conflict 409 -X PUT -H 'Content-Type: application/json' \
  -d "{\"wire\":\"responses\",\"baseURL\":\"http://127.0.0.1:9/v1\",\"models\":[\"probe-model\"],\"expectedGeneration\":0}" \
  "$BASE/api/v1/providers/probe?expectedGeneration=$GEN"
record provider-read-back 200 "$BASE/api/v1/providers"
grep -q "probe-model" "$EVID_WORK/provider-read-back.body" || fail "created provider is missing from providers list"
record provider-update 200 -X PUT -H 'Content-Type: application/json' \
  -d "{\"defaultModel\":\"probe-model\",\"models\":[\"probe-model\"],\"expectedGeneration\":$GEN}" \
  "$BASE/api/v1/providers/probe?expectedGeneration=$GEN"
GEN=$("$PY3" -c 'import json,sys; print(json.load(open(sys.argv[1]))["generation"])' "$EVID_WORK/provider-update.body")
record provider-delete 200 -X DELETE "$BASE/api/v1/providers/probe?expectedGeneration=$GEN"

echo "==> combos and routes mutations"
GEN=$("$PY3" -c 'import json,sys; print(json.load(open(sys.argv[1]))["generation"])' "$EVID_WORK/provider-delete.body")
record combo-put 200 -X PUT -H 'Content-Type: application/json' \
  -d "{\"strategy\":\"failover\",\"targets\":[{\"provider\":\"codex\",\"model\":\"gpt-5.6-luna\"}],\"expectedGeneration\":$GEN}" \
  "$BASE/api/v1/combos/probe-combo?expectedGeneration=$GEN"
GEN=$("$PY3" -c 'import json,sys; print(json.load(open(sys.argv[1]))["generation"])' "$EVID_WORK/combo-put.body")
record route-put 200 -X PUT -H 'Content-Type: application/json' \
  -d "{\"value\":\"codex/gpt-5.6-luna\",\"expectedGeneration\":$GEN}" \
  "$BASE/api/v1/routes/probe-key?expectedGeneration=$GEN"
record routes-list-after 200 "$BASE/api/v1/routes"
grep -q "probe-key" "$EVID_WORK/routes-list-after.body" || fail "set route is missing from routes list"
GEN=$("$PY3" -c 'import json,sys; print(json.load(open(sys.argv[1]))["generation"])' "$EVID_WORK/route-put.body")
record route-delete 200 -X DELETE "$BASE/api/v1/routes/probe-key?expectedGeneration=$GEN"
GEN=$("$PY3" -c 'import json,sys; print(json.load(open(sys.argv[1]))["generation"])' "$EVID_WORK/route-delete.body")
record combo-delete 200 -X DELETE "$BASE/api/v1/combos/probe-combo?expectedGeneration=$GEN"

echo "==> auth start stays pending and status reports it"
record auth-start-codex 200 -X POST "$BASE/api/v1/auth/codex/start"
"$PY3" - "$EVID_WORK/auth-start-codex.body" <<'PYEOF'
import json, sys
d = json.load(open(sys.argv[1]))
assert d.get("session", "") != "", d
assert d.get("url", "").startswith("https://"), d
PYEOF
AUTH_SESSION=$("$PY3" -c 'import json,sys; print(json.load(open(sys.argv[1]))["session"])' "$EVID_WORK/auth-start-codex.body")
record auth-status-codex 200 "$BASE/api/v1/auth/codex/status?session=$AUTH_SESSION"
grep -q "pending" "$EVID_WORK/auth-status-codex.body" || fail "auth status is not pending after start"

GEN=$("$PY3" -c 'import json,sys; print(json.load(open(sys.argv[1]))["generation"])' "$EVID_WORK/combo-delete.body")

echo "==> inference endpoints"
record alias-put 200 -X PUT -H 'Content-Type: application/json' \
  -d "{\"value\":\"codex/gpt-5.6-luna\",\"expectedGeneration\":$GEN}" \
  "$BASE/api/v1/routes/claude-codex--gpt-5-6-luna?expectedGeneration=$GEN"
record v1-models 200 "$BASE/v1/models"
record count-tokens 200 -X POST -H 'Content-Type: application/json' \
  -d '{"model":"claude-codex--gpt-5-6-luna","max_tokens":16,"messages":[{"role":"user","content":[{"type":"text","text":"count me please"}]}]}' \
  "$BASE/v1/messages/count_tokens"
"$PY3" - "$EVID_WORK/count-tokens.body" <<'PYEOF'
import json, sys
d = json.load(open(sys.argv[1]))
assert d.get("input_tokens", 0) > 0, d
PYEOF
record count-tokens-no-route 404 -X POST -H 'Content-Type: application/json' \
  -d '{"model":"claude-none--no-such","max_tokens":16,"messages":[{"role":"user","content":[{"type":"text","text":"count me please"}]}]}' \
  "$BASE/v1/messages/count_tokens"
record compact-no-route 404 -X POST -H 'Content-Type: application/json' \
  -d '{"model":"no-such/model","input":[]}' \
  "$BASE/v1/responses/compact"
record responses-bad-json 400 -X POST -H 'Content-Type: application/json' -d 'not json' "$BASE/v1/responses"
record inference-wrong-method 405 "$BASE/v1/responses"
record unknown-v1 404 "$BASE/v1/frobnicate"
record unknown-root 404 "$BASE/frobnicate"

kill -TERM "$PID"
wait "$PID" || fail "daemon exited nonzero on SIGTERM"

echo "api sweep OK (management reads, negatives, provider/combo/route CAS writes, auth pending, inference shapes)"
