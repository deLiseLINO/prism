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
PORT="${PRISM_PORT:-${PRISMCTL_PROOF_PORT:-18791}}"
RUNDIR=$(mktemp -d /tmp/prism-cliproof.XXXXXX)
RUN_ID="$(date +%Y%m%d-%H%M%S).$$"
EVID_FINAL="${PRISM_VERIFY_EVIDENCE_DIR:-$REPO_ROOT/verify/evidence/prismctl-proof/$RUN_ID}"
EVID_WORK="$RUNDIR/evidence"
mkdir -p "$EVID_WORK" "$RUNDIR/.prism" "$RUNDIR/home"

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

echo "==> building prismd and prismctl"
(cd "$GO_ROOT" && go build -o "$RUNDIR/prismd" ./cmd/prismd) || fail "go build cmd/prismd"
(cd "$GO_ROOT" && go build -o "$RUNDIR/prismctl" ./cmd/prismctl) || fail "go build cmd/prismctl"

# The daemon resolves client config paths from its own HOME, so the isolated
# home must be set on the prismd process itself; a HOME on prismctl alone
# never reaches the apply, which would then read the operator's real
# ~/.grok/config.toml and refuse on user-owned table collisions.
HOME="$RUNDIR/home" "$RUNDIR/prismd" --listen "127.0.0.1:$PORT" --config "$RUNDIR/.prism/prism.json" --credential-store "$RUNDIR/creds" > "$RUNDIR/out.log" 2>&1 &
PID=$!
for _ in $(seq 1 40); do
  curl -sf "http://127.0.0.1:$PORT/api/v1/health" >/dev/null 2>&1 && break
  sleep 0.25
done
curl -sf "http://127.0.0.1:$PORT/api/v1/health" >/dev/null || fail "daemon health failed"

export PRISM_URL="http://127.0.0.1:$PORT"
CTL="$RUNDIR/prismctl"

run_ctl() {
  local name=$1 expect=$2
  shift 2
  set +e
  "$CTL" "$@" > "$EVID_WORK/$name.out" 2> "$EVID_WORK/$name.err"
  local rc=$?
  set -e
  echo "$rc" > "$EVID_WORK/$name.rc"
  [ "$rc" = "$expect" ] || fail "prismctl $name: expected exit $expect, got $rc (stderr: $(cat "$EVID_WORK/$name.err"))"
}

run_ctl_quiet() { run_ctl "$@"; }

echo "==> top-level and read commands"
run_ctl status 0 status
run_ctl status-json 0 status --json
run_ctl doctor 0 doctor
run_ctl doctor-json 0 doctor --json
run_ctl usage 0 usage
run_ctl usage-json 0 usage --json
run_ctl help 0 help
run_ctl providers-list 0 providers list
run_ctl providers-list-json 0 providers list --json
run_ctl accounts-list 0 accounts list
run_ctl accounts-list-json 0 accounts list --json
run_ctl models-list 0 models list
run_ctl models-list-json 0 models list --json
run_ctl combos-list 0 combos list
run_ctl routes-list 0 routes list
run_ctl integrations-status 0 integrations status
run_ctl integrations-status-grok 0 integrations status grok
run_ctl agents-status 0 agents
run_ctl agents-status-grok 0 agents status grok
run_ctl agents-status-json 0 agents status grok --json
run_ctl auth-status 0 auth status

grep -q "gpt-5.6-luna" "$EVID_WORK/models-list.out" || fail "models list does not name a catalog model"
grep -q "codex" "$EVID_WORK/providers-list.out" || fail "providers list does not name codex"
"$PY3" -c 'import json,sys; d=json.load(open(sys.argv[1])); assert isinstance(d.get("providers"), list)' "$EVID_WORK/providers-list-json.out" || fail "providers list --json is not valid JSON"
"$PY3" -c 'import json,sys; d=json.load(open(sys.argv[1])); assert isinstance(d.get("models"), list)' "$EVID_WORK/models-list-json.out" || fail "models list --json is not valid JSON"

echo "==> auth login starts, stays pending, and cancels without any browser or real account"
set +e
"$CTL" auth login codex --no-open > "$EVID_WORK/auth-login.out" 2> "$EVID_WORK/auth-login.err" &
AUTH_PID=$!
( sleep 3; kill -TERM "$AUTH_PID" 2>/dev/null ) &
KILLER=$!
wait "$AUTH_PID"
AUTH_RC=$?
pkill -TERM -P "$KILLER" 2>/dev/null || true
kill "$KILLER" 2>/dev/null || true
set -e
echo "$AUTH_RC" > "$EVID_WORK/auth-login.rc"
[ "$AUTH_RC" = "143" ] || [ "$AUTH_RC" = "1" ] || [ "$AUTH_RC" = "5" ] || fail "prismctl auth login did not stay pending under timeout (exit $AUTH_RC, stderr: $(cat "$EVID_WORK/auth-login.err"))"
grep -q "open this URL" "$EVID_WORK/auth-login.out" || fail "auth login did not print the authorization URL"
"$CTL" auth status > "$EVID_WORK/auth-status-pending.out" 2>&1
grep -q "codex: unauthorized" "$EVID_WORK/auth-status-pending.out" || fail "auth status should report codex unauthorized after cancelled login, got: $(cat "$EVID_WORK/auth-status-pending.out")"
"$CTL" accounts list --json > "$EVID_WORK/accounts-after-cancel.json"
grep -q "codex" "$EVID_WORK/accounts-after-cancel.json" && fail "an account appeared after a cancelled login" || true

echo "==> provider lifecycle through the CLI"
run_ctl providers-add 0 providers add openai-proxy --wire responses --endpoint "http://127.0.0.1:9/v1" --default-model test-model
run_ctl providers-list-after-add 0 providers list
grep -q "openai-proxy" "$EVID_WORK/providers-list-after-add.out" || fail "providers list does not show added provider"
run_ctl providers-edit 0 providers edit openai-proxy --default-model other-model
run_ctl providers-enable 0 providers enable openai-proxy
run_ctl providers-disable 0 providers disable openai-proxy
run_ctl providers-remove 0 providers remove openai-proxy
run_ctl providers-list-after-remove 0 providers list
grep -q "openai-proxy" "$EVID_WORK/providers-list-after-remove.out" && fail "removed provider still listed" || true

echo "==> models enable/disable through the CLI"
run_ctl models-enable 0 models enable codex gpt-5.6-luna
run_ctl models-disable 0 models disable codex gpt-5.6-luna
run_ctl models-renable 0 models enable codex gpt-5.6-luna

echo "==> combo and route lifecycle through the CLI"
run_ctl combos-set 0 combos set mirror --strategy failover --target codex/gpt-5.6-luna
run_ctl combos-list-after 0 combos list
grep -q "mirror" "$EVID_WORK/combos-list-after.out" || fail "combos list does not show set combo"
run_ctl combos-remove 0 combos remove mirror
run_ctl routes-set 0 routes set default codex/gpt-5.6-luna
run_ctl routes-list-after 0 routes list
grep -q "default" "$EVID_WORK/routes-list-after.out" || fail "routes list does not show set route"
run_ctl routes-remove 0 routes remove default

echo "==> account policy actions against the copy-free isolated daemon"
CODEX_ACCOUNT=$("$CTL" accounts list --json | "$PY3" -c 'import json,sys; d=json.load(sys.stdin); a=[x for x in d.get("accounts",[]) if x.get("provider")=="codex"]; print(a[0]["id"] if a else "")')
if [ -n "$CODEX_ACCOUNT" ]; then
  run_ctl accounts-pause 0 accounts pause "$CODEX_ACCOUNT"
  run_ctl accounts-resume 0 accounts resume "$CODEX_ACCOUNT"
  run_ctl accounts-priority 0 accounts priority "$CODEX_ACCOUNT" 5
  run_ctl accounts-quota 0 accounts quota "$CODEX_ACCOUNT"
  run_ctl accounts-remove 0 accounts remove "$CODEX_ACCOUNT"
else
  echo "no codex account on isolated daemon; policy actions skipped" | tee "$EVID_WORK/accounts-policy-skipped.txt"
fi

run_ctl accounts-select 0 accounts select codex auto
run_ctl accounts-auto-switch 0 accounts auto-switch codex on --threshold 0.5
run_ctl accounts-distribute 0 accounts distribute codex round-robin
run_ctl accounts-affinity 0 accounts affinity codex sticky

echo "==> integrations apply and rollback through the CLI"
HOME="$RUNDIR/home" run_ctl integrations-apply-grok 0 integrations apply grok
HOME="$RUNDIR/home" run_ctl integrations-rollback-grok 0 integrations rollback grok
HOME="$RUNDIR/home" run_ctl integrations-status-after 0 integrations status grok

echo "==> usage errors exit 2 and unknown commands exit 2"
run_ctl_quiet usage-bad-provider 2 auth login nonsense
run_ctl_quiet usage-bad-agent 2 agents install nonsense
run_ctl_quiet usage-no-command 2
run_ctl_quiet usage-unknown 2 frobnicate
run_ctl_quiet usage-bad-strategy 2 combos set x --strategy nonsense --target codex/gpt-5.6-luna
run_ctl_quiet usage-bad-wire 2 providers add p --wire nonsense
run_ctl_quiet usage-bad-client 2 integrations apply nonsense

echo "==> daemon unreachable exits 5"
set +e
PRISM_URL="http://127.0.0.1:1" "$CTL" status > "$EVID_WORK/unreachable.out" 2> "$EVID_WORK/unreachable.err"
RC=$?
set -e
[ "$RC" = "5" ] || fail "prismctl status with dead daemon: expected exit 5, got $RC"

kill -TERM "$PID"
wait "$PID" || fail "daemon exited nonzero on SIGTERM"

echo "prismctl proof OK (25 command shapes, usage errors exit 2, unreachable exit 5)"
