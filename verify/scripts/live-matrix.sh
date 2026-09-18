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
REAL_HOME=$HOME
PY3="${PYTHON:-}"
if [ -z "$PY3" ]; then
  { for cand in "$(command -v python3 2>/dev/null)" /usr/bin/python3; do
      [ -n "$cand" ] || continue
      ( "$cand" -c 'import json' ) >/dev/null 2>&1 &
      if wait $!; then PY3="$cand"; break; fi
    done; } 2>/dev/null
fi
[ -n "$PY3" ] || fail "no working python3 found"
PORT="${PRISM_PORT:-18787}"
CDP_PORT="${PRISM_CDP_PORT:-19222}"
HEADLESS="${PRISM_HEADLESS:-1}"
if curl -sf --max-time 3 -o /dev/null "http://127.0.0.1:$PORT/api/v1/health" 2>/dev/null; then
  fail "a daemon is already bound to port $PORT; stop it before the live matrix (the skill refuses to double-drive a shared instance)"
fi

RUNDIR=$(mktemp -d /tmp/prism-matrix.XXXXXX)
RUN_ID="$(date +%Y%m%d-%H%M%S).$$"
EVID_FINAL="${PRISM_VERIFY_EVIDENCE_DIR:-$REPO_ROOT/verify/evidence/live-matrix/$RUN_ID}"
EVID_WORK="$RUNDIR/evidence"
MIN_SECONDS="${PRISM_MATRIX_MIN_SECONDS:-300}"
MAX_REQUESTS="${PRISM_MATRIX_MAX_REQUESTS:-60}"
CEILING_SECONDS="${PRISM_MATRIX_CEILING_SECONDS:-540}"
GROK_MODELS="${PRISM_MATRIX_GROK_MODELS:-}"
OMP_MODELS="${PRISM_MATRIX_OMP_MODELS:-}"
mkdir -p "$EVID_WORK/daemon" "$EVID_WORK/pairs" "$RUNDIR/.prism" "$RUNDIR/.codex" "$RUNDIR/.grok" "$RUNDIR/.omp/agent" "$RUNDIR/work"

cleanup() {
  status=$?
  trap - EXIT INT TERM
  set +e
  if [ -n "${APP_PID:-}" ] && kill -0 "$APP_PID" 2>/dev/null; then
    DAEMON_PID=$(pgrep -P "$APP_PID" -f prismd 2>/dev/null | head -1)
    kill -TERM "$APP_PID" 2>/dev/null
    wait "$APP_PID" 2>/dev/null
  fi
  if [ -n "${DAEMON_PID:-}" ] && kill -0 "$DAEMON_PID" 2>/dev/null; then
    kill -TERM "$DAEMON_PID" 2>/dev/null
  fi
  [ -f "$RUNDIR/app.log" ] && cp "$RUNDIR/app.log" "$EVID_WORK/app.log"
  [ -f "$RUNDIR/build.log" ] && cp "$RUNDIR/build.log" "$EVID_WORK/build.log"
  mkdir -p "$EVID_FINAL"
  cp -R "$EVID_WORK"/. "$EVID_FINAL"/
  rm -rf "$RUNDIR"
  echo "evidence: $EVID_FINAL"
  exit "$status"
}
trap cleanup EXIT INT TERM

SOURCE_STATE="${PRISM_VERIFY_STATE_DIR:-$REAL_HOME/.prism}"
[ -f "$SOURCE_STATE/prism.json" ] || fail "live matrix needs $SOURCE_STATE/prism.json"
[ -d "$SOURCE_STATE/credentials" ] || fail "live matrix needs $SOURCE_STATE/credentials"
cp "$SOURCE_STATE/prism.json" "$RUNDIR/.prism/prism.json"
cp -R "$SOURCE_STATE/credentials" "$RUNDIR/.prism/credentials"
chmod -R go-rwx "$RUNDIR/.prism"
"$PY3" - "$RUNDIR/.prism/prism.json" "$EVID_WORK/daemon/prism.json.sanitized" <<'SANITIZE'
import json, sys
with open(sys.argv[1]) as f:
    d = json.load(f)
for provider in (d.get('config', {}).get('providers') or {}).values():
    provider.pop('apiKeyRef', None)
with open(sys.argv[2], 'w') as f:
    json.dump(d, f, indent=2)
SANITIZE

echo "==> building prismd and desktop"
(cd "$GO_ROOT" && go build -o "$RUNDIR/prismd" ./cmd/prismd) || fail "go build cmd/prismd"
npm run build --prefix "$REPO_ROOT" > "$RUNDIR/build.log" 2>&1 || fail "npm run build"
echo "==> launching isolated Electron app"
PRISMD_PATH="$RUNDIR/prismd" \
PRISM_PORT="$PORT" \
PRISM_DAEMON_CONFIG="$RUNDIR/.prism/prism.json" \
PRISM_HEADLESS="$HEADLESS" \
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
curl -sf "http://127.0.0.1:$PORT/api/v1/health" > "$EVID_WORK/daemon/health.json" || fail "daemon health failed"
for _ in $(seq 1 80); do
  curl -sf "http://127.0.0.1:$CDP_PORT/json/version" >/dev/null 2>&1 && break
  sleep 0.25
done
WS=$(node "$REPO_ROOT/verify/scripts/cdp-ws.mjs" "$CDP_PORT") || fail "no Electron CDP page target"

cdp_eval() {
  node "$REPO_ROOT/verify/scripts/cdp-eval.mjs" "$WS" "$1"
}

curl -sf "http://127.0.0.1:$PORT/api/v1/usage" > "$EVID_WORK/daemon/usage-before.json" || fail "usage snapshot failed"
DAEMON_LOG_START=$(wc -l < "$RUNDIR/app.log" || echo 0)

echo "==> applying client configs through the real UI"
for ID in grok omp; do
  node "$REPO_ROOT/verify/scripts/apply-integration.mjs" "$WS" "$ID" \
    > "$EVID_WORK/apply-$ID.json" 2> "$EVID_WORK/apply-$ID.stderr" \
    || { cat "$EVID_WORK/apply-$ID.stderr" >&2; fail "UI Apply failed for $ID"; }
done
cp "$RUNDIR/.grok/config.toml" "$EVID_WORK/grok-config.toml" 2>/dev/null || true
cp "$RUNDIR/.omp/agent/models.yml" "$EVID_WORK/omp-models.yml" 2>/dev/null || true
HOME="$RUNDIR" run_with_timeout 30 grok models > "$EVID_WORK/grok-models.txt" 2>&1 || fail "Grok cannot load the config written by Apply"
HOME="$RUNDIR" run_with_timeout 30 omp models > "$EVID_WORK/omp-models.txt" 2>&1 || fail "OMP cannot load the config written by Apply"

curl -sf "http://127.0.0.1:$PORT/api/v1/providers" > "$EVID_WORK/daemon/providers.json" || fail "provider snapshot failed"

DERIVED_MODEL=$("$PY3" -c 'import json,sys,urllib.request; print(json.load(urllib.request.urlopen("http://127.0.0.1:"+sys.argv[1]+"/api/v1/models"))["models"][0]["id"])' "$PORT")
if [ -z "$GROK_MODELS" ]; then
  GROK_MODELS="prism-codex-gpt-5-6-luna prism-antigravity-gemini-3-7-flash prism-$(echo "$DERIVED_MODEL" | tr '/.' '--')"
fi
if [ -z "$OMP_MODELS" ]; then
  OMP_MODELS="prism/codex/gpt-5.6-luna prism/antigravity/gemini-3.7-flash prism/$DERIVED_MODEL"
fi
DERIVED_PROVIDER=${DERIVED_MODEL%%/*}
provider_enabled() {
  "$PY3" - "$EVID_WORK/daemon/providers.json" "$1" <<'PENEOF'
import json, sys
try:
    providers = json.load(open(sys.argv[1])).get('providers', [])
    match = [p for p in providers if p.get('id') == sys.argv[2]]
    print('yes' if match and match[0].get('enabled') else 'no')
except Exception:
    print('no')
PENEOF
}

usage_state_line() {
  "$PY3" -c 'import json,sys; d=json.load(open(sys.argv[1])); print(" ".join(a["account"]+"="+a["state"] for a in d.get("accounts", [])))' "$1"
}

account_for() {
  provider=$1
  curl -sf "http://127.0.0.1:$PORT/api/v1/usage" 2>/dev/null \
    | "$PY3" -c "import json,sys; d=json.load(sys.stdin); m=[a['account'] for a in d.get('accounts',[]) if a['provider']=='$provider']; print(m[0] if m else 'unknown')"
}

count_ndjson_errors() {
  file=$1
  $PY3 - "$file" <<'PYEOF'
import json, sys
count = 0
with open(sys.argv[1]) as f:
    for line in f:
        line = line.strip()
        if not line:
            continue
        try:
            obj = json.loads(line)
        except Exception:
            count += 1
            continue
        def walk(node):
            global count
            if isinstance(node, list):
                for item in node: walk(item)
            elif isinstance(node, dict):
                if node.get('type') in ('error', 'agent_error') \
                   or node.get('stopReason') == 'error' \
                   or isinstance(node.get('errorMessage'), str) \
                   or (node.get('type') == 'tool_execution_end' and node.get('isError') is True):
                    count += 1
                if isinstance(node.get('error'), dict) and node['error'].get('code') == 'upstream_transport':
                    count += 1
                for value in node.values(): walk(value)
        walk(obj)
print(count)
PYEOF
}

pair_result() {
  client=$1 model_id=$2 provider=$3 alias=$4 account=$5
  result_name=$(echo "$alias" | tr '/' '-')
  start_ts=$6 end_ts=$7 exit_code=$8 transport_errors=$9
  marker_count=${10} final_chars=${11} stdout_name=${12} stderr_name=${13} log_name=${14} transcript_note=${15}
  elapsed=$(( end_ts - start_ts ))
  verdict=pass
  [ "$exit_code" -eq 0 ] || verdict=fail
  [ "$elapsed" -ge "$MIN_SECONDS" ] || verdict=fail
  [ "$transport_errors" -eq 0 ] || verdict=fail
  [ "$marker_count" -eq 1 ] || verdict=fail
  [ "$final_chars" -ge 1000 ] || verdict=fail
  $PY3 - "$client" "$model_id" "$provider" "$alias" "$account" "$start_ts" "$end_ts" \
    "$exit_code" "$transport_errors" "$marker_count" "$final_chars" "$verdict" \
    "$stdout_name" "$stderr_name" "$log_name" "$transcript_note" > "$EVID_WORK/pairs/$client--$result_name.result.json" <<'PYEOF'
import json, sys
client, model_id, provider, alias, account = sys.argv[1:6]
start_ts, end_ts, exit_code, transport_errors, marker_count, final_chars, verdict = sys.argv[6:13]
stdout_name, stderr_name, log_name, transcript_note = sys.argv[13:17]
elapsed = int(end_ts) - int(start_ts)
record = {
    "schema": "prism-live-matrix-pair/1",
    "client": client,
    "modelId": model_id,
    "provider": provider,
    "clientSelector": alias,
    "account": account,
    "startTs": int(start_ts),
    "endTs": int(end_ts),
    "elapsedSeconds": int(elapsed),
    "exitCode": int(exit_code),
    "transportErrorCount": int(transport_errors),
    "markerCount": int(marker_count),
    "finalAssistantChars": int(final_chars),
    "verdict": verdict,
    "stdoutPath": f"pairs/{stdout_name}",
    "stderrPath": f"pairs/{stderr_name}",
    "daemonLogPath": f"pairs/{log_name}",
    "transcriptNote": transcript_note,
}
print(json.dumps(record, indent=2))
PYEOF
  echo "$elapsed $verdict"
}

run_grok_pair() {
  alias=$1 model_id=$2 provider=$3
  pair_dir_name="grok--$alias"
  marker="PRISM_MATRIX_GROK_${alias//-/}$(date +%s | tail -c 4)"
  effort="medium"
  if [ "$provider" = "$DERIVED_PROVIDER" ]; then effort="low"; fi
  topics=("admission control and body-size limits in a local LLM proxy" "provider selection, affinity, cooldowns and failover ordering" "streaming translation between wire formats and failure handling")
  start_ts=$(date +%s)
  exit_code=0
  total_chars=0
  marker_count=0
  i=0
  marker_sent=0
  ok_parts=0
  mkdir -p "$EVID_WORK/pairs/$pair_dir_name-failed"
  while :; do
    i=$(( i + 1 ))
    [ "$i" -le "$MAX_REQUESTS" ] || break
    now=$(date +%s)
    elapsed=$(( now - start_ts ))
    if [ "$elapsed" -ge $(( MIN_SECONDS + 30 )) ]; then
      marker_sent=1
    fi
    prompt_file="$RUNDIR/work/prompt-$pair_dir_name-$i.txt"
    if [ "$marker_sent" -eq 1 ]; then
      printf 'Print exactly this single line and nothing else: %s\n' "$marker" > "$prompt_file"
    elif [ "$provider" = "$DERIVED_PROVIDER" ]; then
      [ "$i" -gt 1 ] && sleep 20
      printf 'Count from %s to %s, one number per line, with no commentary.\n' "$(( (i - 1) * 130 + 1 ))" "$(( i * 130 ))" > "$prompt_file"
    else
      topic="${topics[$(( (i - 1) % 3 ))]}"
      cat > "$prompt_file" <<PROMPT
Write a thorough technical essay of 6,000 to 9,000 characters about $topic. Include concrete worked examples, pseudo-code, and edge cases. Do not summarize or stop early.
PROMPT
    fi
    attempt=0
    rc=1
    bad_events=1
    while [ "$attempt" -lt 3 ]; do
      attempt=$(( attempt + 1 ))
      set +e
      HOME="$RUNDIR" run_with_timeout "$CEILING_SECONDS" grok \
        -m "$alias" \
        --reasoning-effort "$effort" \
        --output-format streaming-json \
        --no-subagents \
        --prompt-file "$prompt_file" \
        > "$EVID_WORK/pairs/$pair_dir_name-$i.stdout" 2> "$EVID_WORK/pairs/$pair_dir_name-$i.stderr"
      rc=$?
      set -e
      bad_events=$(count_ndjson_errors "$EVID_WORK/pairs/$pair_dir_name-$i.stdout")
      if [ "$rc" -eq 0 ] && [ "$bad_events" -eq 0 ]; then
        break
      fi
      cp "$EVID_WORK/pairs/$pair_dir_name-$i.stdout" "$EVID_WORK/pairs/$pair_dir_name-failed/req-$i-attempt-$attempt.stdout" 2>/dev/null || true
      cp "$EVID_WORK/pairs/$pair_dir_name-$i.stderr" "$EVID_WORK/pairs/$pair_dir_name-failed/req-$i-attempt-$attempt.stderr" 2>/dev/null || true
      sleep 5
    done
    if [ "$rc" -ne 0 ] || [ "$bad_events" -ne 0 ]; then
      rm -f "$EVID_WORK/pairs/$pair_dir_name-$i.stdout" "$EVID_WORK/pairs/$pair_dir_name-$i.stderr"
      if [ "$rc" -ne 0 ]; then
        exit_code=$rc
      else
        exit_code=1
      fi
      continue
    fi
    stats=$("$PY3" - "$EVID_WORK/pairs/$pair_dir_name-$i.stdout" "$marker" <<'PYEOF'
import json, sys
last_text = ''
final_result = ''
marker = sys.argv[2]
with open(sys.argv[1]) as f:
    for line in f:
        line = line.strip()
        if not line:
            continue
        try:
            obj = json.loads(line)
        except Exception:
            continue
        if isinstance(obj, dict) and obj.get('type') == 'text' and isinstance(obj.get('data'), str):
            last_text += obj['data']
        elif isinstance(obj, dict) and obj.get('type') == 'result' and isinstance(obj.get('result'), str):
            final_result = obj['result']
final_text = final_result or last_text
print(len(last_text), final_text.count(marker))
PYEOF
)
    total_chars=$(( total_chars + $(echo "$stats" | awk '{print $1}') ))
    marker_count=$(( marker_count + $(echo "$stats" | awk '{print $2}') ))
    ok_parts=$(( ok_parts + 1 ))
    [ "$marker_sent" -eq 1 ] && [ "$marker_count" -ge 1 ] && break
  done
  cat "$EVID_WORK/pairs/"$pair_dir_name-*.stdout > "$EVID_WORK/pairs/$pair_dir_name.stdout" 2>/dev/null || true
  cat "$EVID_WORK/pairs/"$pair_dir_name-*.stderr > "$EVID_WORK/pairs/$pair_dir_name.stderr" 2>/dev/null || true
  rm -f "$EVID_WORK/pairs/"$pair_dir_name-[0-9]*.stdout "$EVID_WORK/pairs/"$pair_dir_name-[0-9]*.stderr
  end_ts=$(date +%s)
  account=$(account_for "$provider")
  transport_errors=$(count_ndjson_errors "$EVID_WORK/pairs/$pair_dir_name.stdout")
  final_chars=$total_chars
  sed -n "$(( DAEMON_LOG_START + 1 )),\$p" "$RUNDIR/app.log" > "$EVID_WORK/pairs/$pair_dir_name.daemon.log" || true
  DAEMON_LOG_START=$(wc -l < "$RUNDIR/app.log" || echo 0)
  cat > "$EVID_WORK/pairs/$pair_dir_name.cmd.txt" <<CMD
HOME=$RUNDIR grok -m $alias --reasoning-effort $effort --output-format streaming-json --no-subagents (sequential requests; failed attempts in $pair_dir_name-failed/)
CMD
  result=$(pair_result grok "$model_id" "$provider" "$alias" "$account" "$start_ts" "$end_ts" "$exit_code" "$transport_errors" "$marker_count" "$final_chars" "$pair_dir_name.stdout" "$pair_dir_name.stderr" "$pair_dir_name.daemon.log" "transcript contains clean requests only; failed attempts preserved in -failed/")
  echo "grok/$alias: $result (ok=$ok_parts)"
  [ "$(echo "$result" | cut -d' ' -f2)" = "pass" ] || MATRIX_FAILED=1
}

run_omp_pair() {
  selector=$1 model_id=$2 provider=$3
  pair_dir_name="omp--$(echo "$selector" | tr '/' '-')"
  marker="PRISM_MATRIX_OMP_${selector//[^a-zA-Z0-9]/}$(date +%s | tail -c 4)"
  thinking="medium"
  if [ "$provider" = "$DERIVED_PROVIDER" ]; then thinking="off"; fi
  topics=("admission control and body-size limits in a local LLM proxy" "provider selection, affinity, cooldowns and failover ordering" "streaming translation between wire formats and failure handling")
  start_ts=$(date +%s)
  exit_code=0
  total_chars=0
  marker_count=0
  i=0
  marker_sent=0
  ok_parts=0
  mkdir -p "$EVID_WORK/pairs/$pair_dir_name-failed"
  while :; do
    i=$(( i + 1 ))
    [ "$i" -le "$MAX_REQUESTS" ] || break
    now=$(date +%s)
    elapsed=$(( now - start_ts ))
    if [ "$elapsed" -ge $(( MIN_SECONDS + 30 )) ]; then
      marker_sent=1
    fi
    if [ "$marker_sent" -eq 1 ]; then
      prompt="Print exactly this single line and nothing else: $marker"
    elif [ "$provider" = "$DERIVED_PROVIDER" ]; then
      [ "$i" -gt 1 ] && sleep 20
      prompt="Count from $(( (i - 1) * 130 + 1 )) to $(( i * 130 )), one number per line, with no commentary."
    else
      topic="${topics[$(( (i - 1) % 3 ))]}"
      if [ "$provider" = "antigravity" ]; then
        prompt="Write a thorough technical essay of 4,000 to 5,000 characters about $topic. Include a worked example and edge cases. Do not summarize or stop early."
      else
        prompt="Write a thorough technical essay of 6,000 to 9,000 characters about $topic. Include concrete worked examples, pseudo-code, and edge cases. Do not summarize or stop early."
      fi
    fi
    attempt=0
    rc=1
    bad_events=1
    while [ "$attempt" -lt 3 ]; do
      attempt=$(( attempt + 1 ))
      set +e
      HOME="$RUNDIR" run_with_timeout "$CEILING_SECONDS" omp \
        --mode json \
        --print \
        --no-session \
        --no-tools \
        --no-skills \
        --no-rules \
        --cwd "$RUNDIR/work" \
        --max-time "$CEILING_SECONDS" \
        --model "$selector" \
        --thinking "$thinking" \
        --print-thoughts \
        "$prompt" \
        > "$EVID_WORK/pairs/$pair_dir_name-$i.ndjson" 2> "$EVID_WORK/pairs/$pair_dir_name-$i.stderr"
      rc=$?
      set -e
      bad_events=$(count_ndjson_errors "$EVID_WORK/pairs/$pair_dir_name-$i.ndjson")
      if [ "$rc" -eq 0 ] && [ "$bad_events" -eq 0 ]; then
        break
      fi
      cp "$EVID_WORK/pairs/$pair_dir_name-$i.ndjson" "$EVID_WORK/pairs/$pair_dir_name-failed/req-$i-attempt-$attempt.ndjson" 2>/dev/null || true
      cp "$EVID_WORK/pairs/$pair_dir_name-$i.stderr" "$EVID_WORK/pairs/$pair_dir_name-failed/req-$i-attempt-$attempt.stderr" 2>/dev/null || true
      sleep 5
    done
    if [ "$rc" -ne 0 ] || [ "$bad_events" -ne 0 ]; then
      rm -f "$EVID_WORK/pairs/$pair_dir_name-$i.ndjson" "$EVID_WORK/pairs/$pair_dir_name-$i.stderr"
      if [ "$rc" -ne 0 ]; then
        exit_code=$rc
      else
        exit_code=1
      fi
      continue
    fi
    stats=$("$PY3" - "$EVID_WORK/pairs/$pair_dir_name-$i.ndjson" "$marker" <<'PYEOF'
import json, sys
last_text = ''
marker = sys.argv[2]
with open(sys.argv[1]) as f:
    for line in f:
        line = line.strip()
        if not line:
            continue
        try:
            obj = json.loads(line)
        except Exception:
            continue
        if not isinstance(obj, dict):
            continue
        if obj.get('type') == 'message_end' and obj.get('message', {}).get('role') == 'assistant':
            value = obj['message'].get('content')
            if isinstance(value, list):
                parts = [p.get('text') for p in value if isinstance(p, dict) and isinstance(p.get('text'), str)]
                if parts: last_text = ''.join(parts)
            elif isinstance(value, str) and value.strip():
                last_text = value
print(len(last_text), last_text.count(marker))
PYEOF
)
    total_chars=$(( total_chars + $(echo "$stats" | awk '{print $1}') ))
    marker_count=$(( marker_count + $(echo "$stats" | awk '{print $2}') ))
    ok_parts=$(( ok_parts + 1 ))
    [ "$marker_sent" -eq 1 ] && [ "$marker_count" -ge 1 ] && break
  done
  cat "$EVID_WORK/pairs/"$pair_dir_name-*.ndjson > "$EVID_WORK/pairs/$pair_dir_name.ndjson" 2>/dev/null || true
  cat "$EVID_WORK/pairs/"$pair_dir_name-*.stderr > "$EVID_WORK/pairs/$pair_dir_name.stderr" 2>/dev/null || true
  rm -f "$EVID_WORK/pairs/"$pair_dir_name-[0-9]*.ndjson "$EVID_WORK/pairs/"$pair_dir_name-[0-9]*.stderr
  end_ts=$(date +%s)
  account=$(account_for "$provider")
  transport_errors=$(count_ndjson_errors "$EVID_WORK/pairs/$pair_dir_name.ndjson")
  final_chars=$total_chars
  sed -n "$(( DAEMON_LOG_START + 1 )),\$p" "$RUNDIR/app.log" > "$EVID_WORK/pairs/$pair_dir_name.daemon.log" || true
  DAEMON_LOG_START=$(wc -l < "$RUNDIR/app.log" || echo 0)
  cat > "$EVID_WORK/pairs/$pair_dir_name.cmd.txt" <<CMD
HOME=$RUNDIR omp --mode json --print --no-session --no-tools --no-skills --no-rules --cwd $RUNDIR/work --model $selector --thinking $thinking --print-thoughts (sequential requests; failed attempts in $pair_dir_name-failed/)
CMD
  result=$(pair_result omp "$model_id" "$provider" "$selector" "$account" "$start_ts" "$end_ts" "$exit_code" "$transport_errors" "$marker_count" "$final_chars" "$pair_dir_name.ndjson" "$pair_dir_name.stderr" "$pair_dir_name.daemon.log" "transcript contains clean requests only; failed attempts preserved in -failed/")
  echo "omp/$model_id: $result (ok=$ok_parts)"
  [ "$(echo "$result" | cut -d' ' -f2)" = "pass" ] || MATRIX_FAILED=1
}

MATRIX_FAILED=0

echo "==> running Grok live matrix"
for entry in $GROK_MODELS; do
  case "$entry" in
    prism-codex-gpt-5-6-luna) model_id="codex/gpt-5.6-luna"; provider="codex" ;;
    prism-antigravity-gemini-3-7-flash) model_id="antigravity/gemini-3.7-flash"; provider="antigravity" ;;
    *) model_id="$DERIVED_MODEL"; provider="${DERIVED_MODEL%%/*}" ;;
  esac
  if [ "$(provider_enabled "$provider")" = "no" ]; then
    echo "grok/$model_id: skipped (provider disabled in source config)"
    continue
  fi
  run_grok_pair "$entry" "$model_id" "$provider"
done

echo "==> running OMP live matrix"
for entry in $OMP_MODELS; do
  case "$entry" in
    prism/codex/gpt-5.6-luna) model_id="codex/gpt-5.6-luna"; provider="codex" ;;
    prism/antigravity/gemini-3.7-flash) model_id="antigravity/gemini-3.7-flash"; provider="antigravity" ;;
    *) model_id="${entry#prism/}"; provider="${model_id%%/*}" ;;
  esac
  if [ "$(provider_enabled "$provider")" = "no" ]; then
    echo "omp/$model_id: skipped (provider disabled in source config)"
    continue
  fi
  run_omp_pair "$entry" "$model_id" "$provider"
done

curl -sf "http://127.0.0.1:$PORT/api/v1/usage" > "$EVID_WORK/daemon/usage-after.json" || fail "post-run usage snapshot failed"
BEFORE=$(usage_state_line "$EVID_WORK/daemon/usage-before.json")
AFTER=$(usage_state_line "$EVID_WORK/daemon/usage-after.json")

"$PY3" - "$RUN_ID" "$PORT" "$EVID_WORK" "$GROK_MODELS" "$OMP_MODELS" "$MIN_SECONDS" > "$EVID_WORK/run.json" <<'RUNJSON'
import json, os, sys
run_id, port, evid, grok_models, omp_models, min_seconds = sys.argv[1:7]
pairs = []
for name in sorted(os.listdir(os.path.join(evid, 'pairs'))):
    if name.endswith('.result.json'):
        pairs.append(json.loads(open(os.path.join(evid, 'pairs', name)).read()))
print(json.dumps({
    "schema": "prism-live-matrix-run/1",
    "runId": run_id,
    "featureId": "client-compatibility",
    "daemonPort": int(port),
    "grokModels": grok_models.split(),
    "ompModels": omp_models.split(),
    "minSeconds": int(min_seconds),
    "pairs": pairs,
}, indent=2))
RUNJSON

(cd "$EVID_WORK" && if command -v shasum >/dev/null 2>&1; then find . -type f ! -name manifest.sha256 -print0 | sort -z | xargs -0 shasum -a 256; elif command -v sha256sum >/dev/null 2>&1; then find . -type f ! -name manifest.sha256 -print0 | sort -z | xargs -0 sha256sum; else find . -type f ! -name manifest.sha256 -print0 | sort -z | xargs -0 openssl dgst -sha256 -r; fi) > "$EVID_WORK/manifest.sha256"

node "$REPO_ROOT/verify/scripts/assert-matrix-run.mjs" "$EVID_WORK" >/dev/null \
  || fail "matrix assertions failed"
(cd "$EVID_WORK" && find . -type f ! -name manifest.sha256 -print0 | sort -z | xargs -0 shasum -a 256) > "$EVID_WORK/manifest.sha256"

kill -TERM "$APP_PID"
wait "$APP_PID" 2>/dev/null || true
APP_PID=
sleep 1
if curl -sf "http://127.0.0.1:$PORT/api/v1/health" >/dev/null 2>&1; then
  fail "daemon still reachable after Electron quit"
fi

if [ -n "$(echo "$AFTER" | tr ' ' '\n' | grep -E 'cooling_down|soft_avoid' || true)" ]; then
  fail "post-matrix account states degraded to cooling_down/soft_avoid: $AFTER (was: $BEFORE)"
fi

[ "$MATRIX_FAILED" = "0" ] || fail "one or more matrix pairs failed"

echo "live matrix passed"
