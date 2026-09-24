#!/bin/bash
set -euo pipefail

fail() {
  echo "FAIL: $1" >&2
  exit 1
}

run_with_timeout() {
  seconds=$1
  shift
  perl -e '$seconds = shift; alarm $seconds; exec @ARGV' "$seconds" "$@"
}

REPO_ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)
GO_ROOT="${GO_ROOT:-$REPO_ROOT}"
PORT="${PRISM_BIGTASK_PORT:-18814}"
CDP_PORT="${PRISM_BIGTASK_CDP_PORT:-19244}"
REAL_HOME=$HOME
if curl -sf --max-time 3 -o /dev/null "http://127.0.0.1:$PORT/api/v1/health" 2>/dev/null; then
  fail "a daemon is already bound to port $PORT"
fi
SOURCE_STATE="${PRISM_VERIFY_STATE_DIR:-$REAL_HOME/.prism}"
[ -f "$SOURCE_STATE/prism.json" ] || fail "needs $SOURCE_STATE/prism.json"
[ -d "$SOURCE_STATE/credentials" ] || fail "needs $SOURCE_STATE/credentials"

RUN_ID="$(date +%Y%m%d-%H%M%S).$$"
RUNDIR=$(mktemp -d /tmp/prism-bigtask.XXXXXX)
EVID_FINAL="${PRISM_VERIFY_EVIDENCE_DIR:-$REPO_ROOT/verify/evidence/bigtask/$RUN_ID}"
EVID_WORK="$RUNDIR/evidence"
mkdir -p "$EVID_WORK" "$RUNDIR/.prism" "$RUNDIR/.codex" "$RUNDIR/.grok" "$RUNDIR/.omp/agent" \
  "$RUNDIR/.claude" "$RUNDIR/.pi/agent" "$RUNDIR/.config/opencode" "$RUNDIR/.hermes" "$RUNDIR/work"

cleanup() {
  status=$?
  trap - EXIT INT TERM
  set +e
  if [ -n "${APP_PID:-}" ] && kill -0 "$APP_PID" 2>/dev/null; then
    kill -TERM "$APP_PID" 2>/dev/null
    wait "$APP_PID" 2>/dev/null
  fi
  mkdir -p "$EVID_FINAL"
  cp -R "$EVID_WORK"/. "$EVID_FINAL"/
  [ -f "$RUNDIR/app.log" ] && cp "$RUNDIR/app.log" "$EVID_FINAL"/
  rm -rf "$RUNDIR"
  echo "evidence: $EVID_FINAL"
  exit "$status"
}
trap cleanup EXIT INT TERM

cp "$SOURCE_STATE/prism.json" "$RUNDIR/.prism/prism.json"
cp -R "$SOURCE_STATE/credentials" "$RUNDIR/.prism/credentials"
chmod -R go-rwx "$RUNDIR/.prism"

echo "==> building prismd and desktop"
(cd "$GO_ROOT" && go build -o "$RUNDIR/prismd" ./cmd/prismd) || fail "go build cmd/prismd"
npm run build --prefix "$REPO_ROOT" > "$RUNDIR/build.log" 2>&1 || fail "npm run build"

echo "==> launching isolated Electron app"
PRISMD_PATH="$RUNDIR/prismd" \
PRISM_PORT="$PORT" \
PRISM_DAEMON_CONFIG="$RUNDIR/.prism/prism.json" \
PRISM_HEADLESS=1 \
CODEX_HOME="$RUNDIR/.codex" \
HOME="$RUNDIR" \
"$REPO_ROOT/node_modules/.bin/electron" "$REPO_ROOT/apps/desktop" \
  --user-data-dir="$RUNDIR/electron" \
  --remote-debugging-port="$CDP_PORT" > "$RUNDIR/app.log" 2>&1 &
APP_PID=$!
for _ in $(seq 1 80); do
  kill -0 "$APP_PID" 2>/dev/null || { cat "$RUNDIR/app.log" 2>/dev/null; fail "electron exited during startup"; }
  curl -sf "http://127.0.0.1:$PORT/api/v1/health" >/dev/null 2>&1 && break
  sleep 0.25
done
curl -sf "http://127.0.0.1:$PORT/api/v1/health" > "$EVID_WORK/health.json" || fail "daemon health failed"
for _ in $(seq 1 80); do
  curl -sf "http://127.0.0.1:$CDP_PORT/json/version" >/dev/null 2>&1 && break
  sleep 0.25
done
WS=$(node "$REPO_ROOT/verify/scripts/cdp-ws.mjs" "$CDP_PORT") || fail "no Electron CDP page target"

node "$REPO_ROOT/verify/scripts/cdp-eval.mjs" "$WS" "await (async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
  const button = [...document.querySelectorAll('nav button')].find((node) => node.textContent.includes('Experimental'))
  if (!button) throw new Error('Experimental navigation button not found')
  button.click()
  for (let i = 0; i < 40; i++) {
    const input = [...document.querySelectorAll('.experimental-flag')].find((node) => node.textContent.includes('Other agents'))?.querySelector('input')
    if (input) {
      if (!input.checked) input.click()
      return input.checked
    }
    await sleep(100)
  }
  throw new Error('Other agents toggle not found')
})()" > "$EVID_WORK/experimental-agents.json" || fail "could not enable other agents through UI"

echo "==> applying all integrations through the real UI"
for ID in codex grok omp claude pi opencode hermes; do
  node "$REPO_ROOT/verify/scripts/apply-integration.mjs" "$WS" "$ID" \
    > "$EVID_WORK/apply-$ID.json" 2> "$EVID_WORK/apply-$ID.stderr" \
    || { cat "$EVID_WORK/apply-$ID.stderr" >&2; fail "UI Apply failed for $ID"; }
done
cp "$RUNDIR/.grok/config.toml" "$EVID_WORK/grok-config.toml"
cp "$RUNDIR/.omp/agent/models.yml" "$EVID_WORK/omp-models.yml"
cp "$RUNDIR/.claude/settings.json" "$EVID_WORK/claude-settings.json"
cp "$RUNDIR/.pi/agent/models.json" "$EVID_WORK/pi-models.json"
cp "$RUNDIR/.config/opencode/opencode.json" "$EVID_WORK/opencode-config.json"
cp "$RUNDIR/.hermes/config.yaml" "$EVID_WORK/hermes-config.yaml"
cp "$RUNDIR/.codex/config.toml" "$EVID_WORK/codex-config.toml"

MODEL_TASK='Write a complete playable 2D game in a single HTML file: a canvas-based brick breaker with paddle, ball, 5 rows of colored bricks, score display, keyboard controls, win and lose screens, and a restart button. Output the full HTML file contents.'
GAMETASK_MARKER=PRISM_2D_GAME_TASK
PI=prism/$(curl -sf "http://127.0.0.1:$PORT/api/v1/models" | python3 -c 'import json,sys; print(json.load(sys.stdin)["models"][0]["id"])')
BARE_MODEL=${PI#prism/}
GROK_SEL="prism-$(echo "$BARE_MODEL" | tr '/.' '--')"
CLAUDE_SEL="claude-${BARE_MODEL%%/*}--${BARE_MODEL#*/}"
MODEL_LABEL=$BARE_MODEL
CEIL=900

record_result() {
  client=$1; ok=$2; note=$3
  printf '%s\t%s\t%s\n' "$client" "$ok" "$note" >> "$EVID_WORK/results.tsv"
}

echo "==> grok big task ($MODEL_LABEL)"
set +e
HOME="$RUNDIR" run_with_timeout "$CEIL" grok -m "$GROK_SEL" --reasoning-effort low --always-approve \
  --output-format streaming-json --no-subagents \
  -p "$MODEL_TASK Final line must be exactly: $GAMETASK_MARKER" \
  > "$EVID_WORK/grok-game.ndjson" 2> "$EVID_WORK/grok-game.stderr"
rc=$?
set -e
final_len=$(python3 - "$EVID_WORK/grok-game.ndjson" "$GAMETASK_MARKER" <<'EOF'
import json, sys
marker = sys.argv[2]
streamed = []
last_message = ''
for line in open(sys.argv[1]):
    line = line.strip()
    if not line: continue
    try: obj = json.loads(line)
    except Exception: continue
    if obj.get('type') == 'text':
        streamed.append(obj.get('data', ''))
    if obj.get('type') == 'message_end':
        for c in obj.get('message', {}).get('content', []):
            if c.get('type') == 'text': last_message = c.get('text', '')
body = last_message if last_message else ''.join(streamed)
print(len(body), 1 if marker in body else 0)
EOF
)
final_chars=$(echo "$final_len" | cut -d' ' -f1)
final_marker=$(echo "$final_len" | cut -d' ' -f2)
rm -f "$REPO_ROOT/brick-breaker.html" "$REPO_ROOT/brick_breaker.html" "$REPO_ROOT/prism-breaker.html"
if [ "$rc" -eq 0 ] && [ "$final_chars" -ge 2000 ] && [ "$final_marker" = "1" ]; then
  record_result grok pass "chars=$final_chars"
else
  record_result grok fail "rc=$rc chars=$final_chars marker=$final_marker"
fi

echo "==> omp big task ($MODEL_LABEL, thinking high)"
set +e
HOME="$RUNDIR" run_with_timeout "$CEIL" omp --mode json --print --no-session --no-skills --no-rules \
  --cwd "$RUNDIR/work" --model "$PI" --thinking high --print-thoughts \
  "$MODEL_TASK Final line must be exactly: $GAMETASK_MARKER" \
  > "$EVID_WORK/omp-game.ndjson" 2> "$EVID_WORK/omp-game.stderr"
rc=$?
set -e
omp_total=$(python3 - "$EVID_WORK/omp-game.ndjson" "$GAMETASK_MARKER" <<'OMPEOF'
import json, sys
marker = sys.argv[2]
total = 0
has_marker = False
errors = 0
with open(sys.argv[1]) as f:
    for line in f:
        line = line.strip()
        if not line: continue
        try: obj = json.loads(line)
        except Exception: continue
        if obj.get('type') == 'message_end' and obj.get('message', {}).get('role') == 'assistant':
            for c in obj.get('message', {}).get('content', []):
                if c.get('type') == 'text':
                    total += len(c.get('text', ''))
                    if marker in c.get('text', ''): has_marker = True
        if obj.get('type') == 'error' or obj.get('type') == 'agent_error' or obj.get('stopReason') == 'error':
            errors += 1
print(f"{total} {int(has_marker)} {errors}")
OMPEOF
)
omp_total_chars=$(echo "$omp_total" | cut -d' ' -f1)
omp_marker=$(echo "$omp_total" | cut -d' ' -f2)
omp_errors=$(echo "$omp_total" | cut -d' ' -f3)
if [ "$rc" -eq 0 ] && [ "$omp_total_chars" -ge 2000 ] && [ "$omp_marker" = "1" ] && [ "$omp_errors" = "0" ]; then
  record_result omp pass "chars=$omp_total_chars marker asserted"
else
  record_result omp fail "rc=$rc chars=$omp_total_chars marker=$omp_marker errors=$omp_errors"
fi

echo "==> claude big task ($MODEL_LABEL through claude-alias)"
set +e
HOME="$RUNDIR" run_with_timeout "$CEIL" claude -p "$MODEL_TASK Final line must be exactly: $GAMETASK_MARKER" \
  --model "$CLAUDE_SEL" > "$EVID_WORK/claude-game.txt" 2> "$EVID_WORK/claude-game.stderr"
rc=$?
set -e
chars=$(wc -c < "$EVID_WORK/claude-game.txt" | tr -d ' ')
if [ "$rc" -eq 0 ] && [ "$chars" -ge 2000 ] && grep -q "$GAMETASK_MARKER" "$EVID_WORK/claude-game.txt"; then
  record_result claude pass "chars=$chars"
else
  record_result claude fail "rc=$rc chars=$chars"
fi

echo "==> pi big task ($MODEL_LABEL)"
set +e
HOME="$RUNDIR" run_with_timeout "$CEIL" pi --provider prism --model "$BARE_MODEL" \
  -p "$MODEL_TASK Final line must be exactly: $GAMETASK_MARKER" \
  > "$EVID_WORK/pi-game.txt" 2> "$EVID_WORK/pi-game.stderr"
rc=$?
set -e
chars=$(wc -c < "$EVID_WORK/pi-game.txt" | tr -d ' ')
if [ "$rc" -eq 0 ] && [ "$chars" -ge 2000 ] && grep -q "$GAMETASK_MARKER" "$EVID_WORK/pi-game.txt"; then
  record_result pi pass "chars=$chars"
else
  record_result pi fail "rc=$rc chars=$chars"
fi

echo "==> opencode big task ($MODEL_LABEL)"
set +e
(
  unset OPENCODE_CONFIG_DIR
  export HOME="$RUNDIR"
  run_with_timeout "$CEIL" opencode run --standalone --model "$PI" "$MODEL_TASK Final line must be exactly: $GAMETASK_MARKER" \
    > "$EVID_WORK/opencode-game.txt" 2> "$EVID_WORK/opencode-game.stderr"
)
rc=$?
set -e
chars=$(wc -c < "$EVID_WORK/opencode-game.txt" | tr -d ' ')
game_artifact=0
[ -s "$RUNDIR/work/brick-breaker.html" ] && game_artifact=$(wc -c < "$RUNDIR/work/brick-breaker.html" | tr -d ' ')
if [ "$rc" -eq 0 ] && grep -q "$GAMETASK_MARKER" "$EVID_WORK/opencode-game.txt" \
   && { [ "$chars" -ge 2000 ] || [ "$game_artifact" -ge 5000 ]; }; then
  record_result opencode pass "chars=$chars artifact=$game_artifact"
else
  record_result opencode fail "rc=$rc chars=$chars artifact=$game_artifact"
fi

echo "==> hermes big task ($MODEL_LABEL)"
set +e
HOME="$RUNDIR" run_with_timeout "$CEIL" hermes chat --provider prism -m "$BARE_MODEL" --ignore-user-config \
  -q "$MODEL_TASK Final line must be exactly: $GAMETASK_MARKER" \
  > "$EVID_WORK/hermes-game.txt" 2> "$EVID_WORK/hermes-game.stderr"
set -e
chars=$(wc -c < "$EVID_WORK/hermes-game.txt" | tr -d ' ')
if [ "$rc" -eq 0 ] && [ "$chars" -ge 2000 ] && grep -q "$GAMETASK_MARKER" "$EVID_WORK/hermes-game.txt"; then
  record_result hermes pass "chars=$chars"
else
  record_result hermes fail "rc=$rc chars=$chars"
fi

echo "==> codex big task ($MODEL_LABEL through prism provider)"
set +e
OPENAI_API_KEY=dummy HOME="$RUNDIR" run_with_timeout "$CEIL" codex exec --skip-git-repo-check -s read-only \
  -m "$BARE_MODEL" "$MODEL_TASK Final line must be exactly: $GAMETASK_MARKER" \
  > "$EVID_WORK/codex-game.txt" 2> "$EVID_WORK/codex-game.stderr"
set -e
chars=$(wc -c < "$EVID_WORK/codex-game.txt" | tr -d ' ')
if [ "$rc" -eq 0 ] && [ "$chars" -ge 2000 ] && grep -q "$GAMETASK_MARKER" "$EVID_WORK/codex-game.txt"; then
  record_result codex pass "chars=$chars"
else
  record_result codex fail "rc=$rc chars=$chars"
fi

echo "==> results"
cat "$EVID_WORK/results.tsv"
if grep -q $'\tfail\t' "$EVID_WORK/results.tsv"; then
  fail "one or more clients failed the big task"
fi

curl -sf "http://127.0.0.1:$PORT/api/v1/requests" > "$EVID_WORK/requests.json" || true
curl -sf "http://127.0.0.1:$PORT/api/v1/usage" > "$EVID_WORK/usage-after.json" || true
echo "big task verification passed"
