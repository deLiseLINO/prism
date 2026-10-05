#!/bin/bash
set -euo pipefail

fail() {
  echo "FAIL: $1" >&2
  exit 1
}

run_with_timeout() {
  seconds=$1
  shift
  env -i "${_VERIFY_ENV[@]}" perl -e '$seconds = shift; alarm $seconds; exec @ARGV' "$seconds" "$@"
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
source "$REPO_ROOT/verify/scripts/owned-runtime.sh"
verify_init live-matrix "$PORT" "$CDP_PORT"
RUN_ID="$(date +%Y%m%d-%H%M%S).$$"
PRIVATE_WORK="$RUNDIR/private-matrix"
MIN_SECONDS="${PRISM_MATRIX_MIN_SECONDS:-300}"
MAX_REQUESTS="${PRISM_MATRIX_MAX_REQUESTS:-60}"
CEILING_SECONDS="${PRISM_MATRIX_CEILING_SECONDS:-540}"
GROK_MODELS="${PRISM_MATRIX_GROK_MODELS:-}"
OMP_MODELS="${PRISM_MATRIX_OMP_MODELS:-}"
mkdir -p "$PRIVATE_WORK/daemon" "$PRIVATE_WORK/pairs" "$RUNDIR/.codex" "$RUNDIR/.grok" "$RUNDIR/.omp/agent" "$RUNDIR/work"

SOURCE_STATE="${PRISM_VERIFY_STATE_DIR:-$REAL_HOME/.prism}"
VERIFY_LIVE=1
verify_stage_state "$SOURCE_STATE"

echo "==> building prism and desktop"
(cd "$GO_ROOT" && go build -o "$RUNDIR/prism" ./cmd/prism) || fail "go build cmd/prism"
npm run build --prefix "$REPO_ROOT" > "$RUNDIR/build.log" 2>&1 || fail "npm run build"
echo "==> launching isolated Electron app"
verify_start_electron
curl -sf --max-time 5 "http://127.0.0.1:$PORT/api/v1/health" > "$PRIVATE_WORK/daemon/health.json" || fail "daemon health failed"

cdp_eval() {
  node "$REPO_ROOT/verify/scripts/cdp-eval.mjs" "$WS" "$1" "${2:-30000}"
}

curl -sf "http://127.0.0.1:$PORT/api/v1/usage" > "$PRIVATE_WORK/daemon/usage-before.json" || fail "usage snapshot failed"
DAEMON_LOG="$RUNDIR/.prism/prism.log"
DAEMON_LOG_START=$(wc -l < "$DAEMON_LOG")

echo "==> enabling Other agents through the real UI"
cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
  const nav = [...document.querySelectorAll('nav button')].find((node) => node.textContent.includes('Experimental'))
  if (!nav) throw new Error('Experimental navigation button not found')
  nav.click()
  for (let i = 0; i < 40; i++) {
    const row = [...document.querySelectorAll('.experimental-flag')].find((node) => node.textContent.includes('Other agents'))
    const input = row?.querySelector('input')
    if (input) {
      if (!input.checked) input.click()
      if (!input.checked) throw new Error('Other agents did not enable')
      return {enabled: true}
    }
    await sleep(100)
  }
  throw new Error('Other agents toggle not found')
})()" > "$EVID_WORK/experimental-agents.json"

echo "==> applying client configs through the real UI"
for ID in grok omp; do
  node "$REPO_ROOT/verify/scripts/apply-integration.mjs" "$WS" "$ID" \
    > "$PRIVATE_WORK/apply-$ID.json" 2> "$PRIVATE_WORK/apply-$ID.stderr" \
    || fail "UI Apply failed for $ID; diagnostics remain private"
done
HOME="$RUNDIR" run_with_timeout 30 grok models > "$PRIVATE_WORK/grok-models.txt" 2>&1 || fail "Grok cannot load the config written by Apply"
HOME="$RUNDIR" run_with_timeout 30 omp models > "$PRIVATE_WORK/omp-models.txt" 2>&1 || fail "OMP cannot load the config written by Apply"

curl -sf "http://127.0.0.1:$PORT/api/v1/providers" > "$PRIVATE_WORK/daemon/providers.json" || fail "provider snapshot failed"

DERIVED_MODEL=$("$PY3" -c 'import json,sys,urllib.request; print(json.load(urllib.request.urlopen("http://127.0.0.1:"+sys.argv[1]+"/api/v1/models"))["models"][0]["id"])' "$PORT")
if [ -z "$GROK_MODELS" ]; then
  GROK_MODELS="prism-codex-gpt-5-6-luna prism-antigravity-gemini-3-7-flash prism-$(echo "$DERIVED_MODEL" | tr '/.' '--')"
fi
if [ -z "$OMP_MODELS" ]; then
  OMP_MODELS="prism/codex/gpt-5.6-luna prism/antigravity/gemini-3.7-flash prism/$DERIVED_MODEL"
fi
DERIVED_PROVIDER=${DERIVED_MODEL%%/*}
provider_enabled() {
  "$PY3" - "$PRIVATE_WORK/daemon/providers.json" "$1" <<'PENEOF'
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
  failed_attempts=${16} failed_attempt_errors=${17} attempts=${18} requests=${19}
  elapsed=$(( end_ts - start_ts ))
  verdict=pass
  [ "$exit_code" -eq 0 ] || verdict=fail
  [ "$elapsed" -ge "$MIN_SECONDS" ] || verdict=fail
  [ "$transport_errors" -eq 0 ] || verdict=fail
  [ "$failed_attempts" -eq 0 ] || verdict=fail
  [ "$marker_count" -eq 1 ] || verdict=fail
  [ "$final_chars" -ge 1000 ] || verdict=fail
  $PY3 - "$client" "$model_id" "$provider" "$alias" "$account" "$start_ts" "$end_ts" \
    "$exit_code" "$transport_errors" "$marker_count" "$final_chars" "$verdict" \
    "$stdout_name" "$stderr_name" "$log_name" "$transcript_note" \
    "$failed_attempts" "$failed_attempt_errors" "$attempts" "$requests" > "$PRIVATE_WORK/pairs/$client--$result_name.result.json" <<'PYEOF'
import json, sys
client, model_id, provider, alias, account = sys.argv[1:6]
start_ts, end_ts, exit_code, transport_errors, marker_count, final_chars, verdict = sys.argv[6:13]
stdout_name, stderr_name, log_name, transcript_note = sys.argv[13:17]
failed_attempts, failed_attempt_errors, attempts, requests = map(int, sys.argv[17:21])
elapsed = int(end_ts) - int(start_ts)
record = {
    "schema": "prism-live-matrix-pair/1",
    "scope": "repeated-request stress",
    "continuousSession": False,
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
    "failedAttemptCount": failed_attempts,
    "failedAttemptErrorCount": failed_attempt_errors,
    "attemptCount": attempts,
    "requestCount": requests,
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
  failed_attempts=0
  failed_attempt_errors=0
  attempts=0
  requests=0
  mkdir -p "$PRIVATE_WORK/pairs/$pair_dir_name-failed"
  while :; do
    i=$(( i + 1 ))
    [ "$i" -le "$MAX_REQUESTS" ] || break
    requests=$i
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
      attempts=$(( attempts + 1 ))
      set +e
      HOME="$RUNDIR" run_with_timeout "$CEILING_SECONDS" grok \
        -m "$alias" \
        --reasoning-effort "$effort" \
        --output-format streaming-json \
        --no-subagents \
        --prompt-file "$prompt_file" \
        > "$PRIVATE_WORK/pairs/$pair_dir_name-$i.stdout" 2> "$PRIVATE_WORK/pairs/$pair_dir_name-$i.stderr"
      rc=$?
      set -e
      bad_events=$(count_ndjson_errors "$PRIVATE_WORK/pairs/$pair_dir_name-$i.stdout")
      if [ "$rc" -eq 0 ] && [ "$bad_events" -eq 0 ]; then
        break
      fi
      failed_attempts=$(( failed_attempts + 1 ))
      failed_attempt_errors=$(( failed_attempt_errors + bad_events ))
      cp "$PRIVATE_WORK/pairs/$pair_dir_name-$i.stdout" "$PRIVATE_WORK/pairs/$pair_dir_name-failed/req-$i-attempt-$attempt.stdout"
      cp "$PRIVATE_WORK/pairs/$pair_dir_name-$i.stderr" "$PRIVATE_WORK/pairs/$pair_dir_name-failed/req-$i-attempt-$attempt.stderr"
      sleep 5
    done
    if [ "$rc" -ne 0 ] || [ "$bad_events" -ne 0 ]; then
      rm -f "$PRIVATE_WORK/pairs/$pair_dir_name-$i.stdout" "$PRIVATE_WORK/pairs/$pair_dir_name-$i.stderr"
      if [ "$rc" -ne 0 ]; then
        exit_code=$rc
      else
        exit_code=1
      fi
      continue
    fi
    stats=$("$PY3" - "$PRIVATE_WORK/pairs/$pair_dir_name-$i.stdout" "$marker" <<'PYEOF'
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
  cat "$PRIVATE_WORK/pairs/"$pair_dir_name-*.stdout > "$PRIVATE_WORK/pairs/$pair_dir_name.stdout" 2>/dev/null || true
  cat "$PRIVATE_WORK/pairs/"$pair_dir_name-*.stderr > "$PRIVATE_WORK/pairs/$pair_dir_name.stderr" 2>/dev/null || true
  rm -f "$PRIVATE_WORK/pairs/"$pair_dir_name-[0-9]*.stdout "$PRIVATE_WORK/pairs/"$pair_dir_name-[0-9]*.stderr
  end_ts=$(date +%s)
  account=$(account_for "$provider")
  transport_errors=$(( $(count_ndjson_errors "$PRIVATE_WORK/pairs/$pair_dir_name.stdout") + failed_attempt_errors ))
  final_chars=$total_chars
  sed -n "$(( DAEMON_LOG_START + 1 )),\$p" "$DAEMON_LOG" > "$PRIVATE_WORK/pairs/$pair_dir_name.daemon.log"
  DAEMON_LOG_START=$(wc -l < "$DAEMON_LOG")
  cat > "$PRIVATE_WORK/pairs/$pair_dir_name.cmd.txt" <<CMD
HOME=$RUNDIR grok -m $alias --reasoning-effort $effort --output-format streaming-json --no-subagents (sequential requests; failed attempts in $pair_dir_name-failed/)
CMD
  result=$(pair_result grok "$model_id" "$provider" "$alias" "$account" "$start_ts" "$end_ts" "$exit_code" "$transport_errors" "$marker_count" "$final_chars" "$pair_dir_name.stdout" "$pair_dir_name.stderr" "$pair_dir_name.daemon.log" "transcript contains successful requests only; failed attempts counted and preserved in -failed/" "$failed_attempts" "$failed_attempt_errors" "$attempts" "$requests")
  echo "grok repeated-request stress: $result (ok=$ok_parts, failedAttempts=$failed_attempts)"
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
  failed_attempts=0
  failed_attempt_errors=0
  attempts=0
  requests=0
  mkdir -p "$PRIVATE_WORK/pairs/$pair_dir_name-failed"
  while :; do
    i=$(( i + 1 ))
    [ "$i" -le "$MAX_REQUESTS" ] || break
    requests=$i
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
      attempts=$(( attempts + 1 ))
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
        > "$PRIVATE_WORK/pairs/$pair_dir_name-$i.ndjson" 2> "$PRIVATE_WORK/pairs/$pair_dir_name-$i.stderr"
      rc=$?
      set -e
      bad_events=$(count_ndjson_errors "$PRIVATE_WORK/pairs/$pair_dir_name-$i.ndjson")
      if [ "$rc" -eq 0 ] && [ "$bad_events" -eq 0 ]; then
        break
      fi
      failed_attempts=$(( failed_attempts + 1 ))
      failed_attempt_errors=$(( failed_attempt_errors + bad_events ))
      cp "$PRIVATE_WORK/pairs/$pair_dir_name-$i.ndjson" "$PRIVATE_WORK/pairs/$pair_dir_name-failed/req-$i-attempt-$attempt.ndjson"
      cp "$PRIVATE_WORK/pairs/$pair_dir_name-$i.stderr" "$PRIVATE_WORK/pairs/$pair_dir_name-failed/req-$i-attempt-$attempt.stderr"
      sleep 5
    done
    if [ "$rc" -ne 0 ] || [ "$bad_events" -ne 0 ]; then
      rm -f "$PRIVATE_WORK/pairs/$pair_dir_name-$i.ndjson" "$PRIVATE_WORK/pairs/$pair_dir_name-$i.stderr"
      if [ "$rc" -ne 0 ]; then
        exit_code=$rc
      else
        exit_code=1
      fi
      continue
    fi
    stats=$("$PY3" - "$PRIVATE_WORK/pairs/$pair_dir_name-$i.ndjson" "$marker" <<'PYEOF'
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
  cat "$PRIVATE_WORK/pairs/"$pair_dir_name-*.ndjson > "$PRIVATE_WORK/pairs/$pair_dir_name.ndjson" 2>/dev/null || true
  cat "$PRIVATE_WORK/pairs/"$pair_dir_name-*.stderr > "$PRIVATE_WORK/pairs/$pair_dir_name.stderr" 2>/dev/null || true
  rm -f "$PRIVATE_WORK/pairs/"$pair_dir_name-[0-9]*.ndjson "$PRIVATE_WORK/pairs/"$pair_dir_name-[0-9]*.stderr
  end_ts=$(date +%s)
  account=$(account_for "$provider")
  transport_errors=$(( $(count_ndjson_errors "$PRIVATE_WORK/pairs/$pair_dir_name.ndjson") + failed_attempt_errors ))
  final_chars=$total_chars
  sed -n "$(( DAEMON_LOG_START + 1 )),\$p" "$DAEMON_LOG" > "$PRIVATE_WORK/pairs/$pair_dir_name.daemon.log"
  DAEMON_LOG_START=$(wc -l < "$DAEMON_LOG")
  cat > "$PRIVATE_WORK/pairs/$pair_dir_name.cmd.txt" <<CMD
HOME=$RUNDIR omp --mode json --print --no-session --no-tools --no-skills --no-rules --cwd $RUNDIR/work --model $selector --thinking $thinking --print-thoughts (sequential requests; failed attempts in $pair_dir_name-failed/)
CMD
  result=$(pair_result omp "$model_id" "$provider" "$selector" "$account" "$start_ts" "$end_ts" "$exit_code" "$transport_errors" "$marker_count" "$final_chars" "$pair_dir_name.ndjson" "$pair_dir_name.stderr" "$pair_dir_name.daemon.log" "transcript contains successful requests only; failed attempts counted and preserved in -failed/" "$failed_attempts" "$failed_attempt_errors" "$attempts" "$requests")
  echo "omp repeated-request stress: $result (ok=$ok_parts, failedAttempts=$failed_attempts)"
  [ "$(echo "$result" | cut -d' ' -f2)" = "pass" ] || MATRIX_FAILED=1
}

MATRIX_FAILED=0

echo "==> running Grok repeated-request stress matrix"
for entry in $GROK_MODELS; do
  case "$entry" in
    prism-codex-gpt-5-6-luna) model_id="codex/gpt-5.6-luna"; provider="codex" ;;
    prism-antigravity-gemini-3-7-flash) model_id="antigravity/gemini-3.7-flash"; provider="antigravity" ;;
    *) model_id="$DERIVED_MODEL"; provider="${DERIVED_MODEL%%/*}" ;;
  esac
  if [ "$(provider_enabled "$provider")" = "no" ]; then
    echo "grok pair skipped (provider disabled in source config)"
    continue
  fi
  run_grok_pair "$entry" "$model_id" "$provider"
done

echo "==> running OMP repeated-request stress matrix"
for entry in $OMP_MODELS; do
  case "$entry" in
    prism/codex/gpt-5.6-luna) model_id="codex/gpt-5.6-luna"; provider="codex" ;;
    prism/antigravity/gemini-3.7-flash) model_id="antigravity/gemini-3.7-flash"; provider="antigravity" ;;
    *) model_id="${entry#prism/}"; provider="${model_id%%/*}" ;;
  esac
  if [ "$(provider_enabled "$provider")" = "no" ]; then
    echo "omp pair skipped (provider disabled in source config)"
    continue
  fi
  run_omp_pair "$entry" "$model_id" "$provider"
done

curl -sf "http://127.0.0.1:$PORT/api/v1/usage" > "$PRIVATE_WORK/daemon/usage-after.json" || fail "post-run usage snapshot failed"
BEFORE=$(usage_state_line "$PRIVATE_WORK/daemon/usage-before.json")
AFTER=$(usage_state_line "$PRIVATE_WORK/daemon/usage-after.json")

"$PY3" - "$RUN_ID" "$PORT" "$PRIVATE_WORK" "$GROK_MODELS" "$OMP_MODELS" "$MIN_SECONDS" > "$PRIVATE_WORK/run.json" <<'RUNJSON'
import json, os, sys
run_id, port, evid, grok_models, omp_models, min_seconds = sys.argv[1:7]
pairs = []
for name in sorted(os.listdir(os.path.join(evid, 'pairs'))):
    if name.endswith('.result.json'):
        pairs.append(json.loads(open(os.path.join(evid, 'pairs', name)).read()))
print(json.dumps({
    "schema": "prism-live-matrix-run/1",
    "scope": "repeated-request stress",
    "continuousSession": False,
    "durationIncludes": ["requests", "retries", "delays"],
    "runId": run_id,
    "featureId": "client-compatibility",
    "daemonPort": int(port),
    "grokModels": grok_models.split(),
    "ompModels": omp_models.split(),
    "minSeconds": int(min_seconds),
    "pairs": pairs,
}, indent=2))
RUNJSON

(cd "$PRIVATE_WORK" && if command -v shasum >/dev/null 2>&1; then find . -type f ! -name manifest.sha256 -print0 | sort -z | xargs -0 shasum -a 256; elif command -v sha256sum >/dev/null 2>&1; then find . -type f ! -name manifest.sha256 -print0 | sort -z | xargs -0 sha256sum; else find . -type f ! -name manifest.sha256 -print0 | sort -z | xargs -0 openssl dgst -sha256 -r; fi) > "$PRIVATE_WORK/manifest.sha256"

ASSERTION_STATUS=0
node "$REPO_ROOT/verify/scripts/assert-matrix-run.mjs" "$PRIVATE_WORK" > "$RUNDIR/matrix-assertion.stdout" 2> "$RUNDIR/matrix-assertion.stderr" \
  || ASSERTION_STATUS=$?

"$PY3" - "$PRIVATE_WORK/run.json" "$EVID_WORK/run.json" "$ASSERTION_STATUS" "$MATRIX_FAILED" "$BEFORE" "$AFTER" <<'SUMMARY'
import json, sys
run = json.load(open(sys.argv[1]))
degraded = any(item.rsplit('=', 1)[-1] in ('cooling_down', 'soft_avoid') for item in sys.argv[6].split())
json.dump({
    'schema': 'prism-live-matrix-summary/1',
    'runId': run['runId'],
    'scope': run['scope'],
    'continuousSession': False,
    'durationIncludes': run['durationIncludes'],
    'minSeconds': run['minSeconds'],
    'assertions': 'passed' if sys.argv[3] == '0' else 'failed',
    'pairChecks': 'passed' if sys.argv[4] == '0' else 'failed',
    'accountStateCheck': 'failed' if degraded else 'passed',
    'accountStatesChanged': sys.argv[5] != sys.argv[6],
    'verdict': 'unavailable' if not run['pairs'] else ('pass' if sys.argv[3:5] == ['0', '0'] and not degraded else 'fail'),
    'rawEvidence': 'private, no sanitizer contract',
    'pairs': [{k: pair[k] for k in ('client', 'elapsedSeconds', 'exitCode', 'transportErrorCount', 'failedAttemptCount', 'failedAttemptErrorCount', 'attemptCount', 'requestCount', 'markerCount', 'finalAssistantChars', 'verdict')} for pair in run['pairs']],
}, open(sys.argv[2], 'w'), indent=2)
SUMMARY

echo "==> quitting Electron, attesting the shared daemon, then stopping it explicitly"
verify_quit_app
verify_attest_daemon
verify_stop_daemon

if ! "$PY3" -c 'import json,sys; sys.exit(0 if json.load(open(sys.argv[1]))["pairs"] else 1)' "$PRIVATE_WORK/run.json"; then
  echo "VERIFIED_UNREACHABLE: no enabled requested provider pairs" >&2
  exit 3
fi

if [ -n "$(echo "$AFTER" | tr ' ' '\n' | grep -E 'cooling_down|soft_avoid' || true)" ]; then
  fail "post-matrix account states degraded to cooling_down/soft_avoid; details remain private"
fi

[ "$ASSERTION_STATUS" = "0" ] || fail "matrix assertions failed; diagnostics remain private"
[ "$MATRIX_FAILED" = "0" ] || fail "one or more matrix pairs failed"

echo "repeated-request stress matrix passed"
