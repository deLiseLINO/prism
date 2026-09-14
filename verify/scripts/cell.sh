#!/usr/bin/env bash
# One disposable-container cell of the prism agent-install test matrix.
#
# The host driver (verify/scripts/matrix.sh) mounts the repo read-only at /repo,
# prebuilt prism binaries at /dist and a writable evidence dir at /evidence,
# then runs this script with the environment below. Every API answer is kept
# as raw JSON; outcome.txt carries the cell verdict.
#
#   CELL         evidence label (required)
#   PORT         prismd listen port (required)
#   DAEMON_PATH  PATH the daemon resolves tools and agents on (default: inherit)
#   PRE          setup run before the daemon starts (optional)
#   MID          setup run after the first status snapshot (optional)
#   AGENT        agent key installed through the API (optional)
#   UPDATE_AGENT agent key updated through the API (optional)
set -uo pipefail

CELL=${CELL:?CELL required}
PORT=${PORT:?PORT required}
EV=/evidence
URL=http://127.0.0.1:$PORT

mkdir -p "$EV"
exec > >(tee -a "$EV/cell.log") 2>&1

log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
prism() { PRISM_URL="$URL" /dist/prismctl "$@"; }

daemon_path() {
  local p=${DAEMON_PATH:-$PATH}
  printf '%s:%s/.local/bin:%s/.opencode/bin:%s/.claude/local:%s/.grok/downloads:%s/.codex/bin' \
    "$p" "$HOME" "$HOME" "$HOME" "$HOME" "$HOME"
}

job_state() { jq -r '.job.state // "none"' "$1" 2>/dev/null || echo none; }

fail() { printf 'cell=%s verdict=fail reason=%s\n' "$CELL" "$*" > "$EV/outcome.txt"; exit 1; }

if [ -n "${PRE:-}" ]; then
  log "PRE: $PRE"
  bash -c "$PRE" || fail "pre-daemon setup failed"
fi

DP=$(daemon_path)
mkdir -p /tmp/prism-state
log "starting prismd on $PORT (PATH=$DP)"
PRISM_AGENT_ACTIONS=1 PATH="$DP" /dist/prismd \
  -listen "127.0.0.1:$PORT" \
  -config /tmp/prism-state/prism.json \
  -credential-store /tmp/prism-state/creds &
DAEMON_PID=$!

for _ in $(seq 1 50); do
  curl -sf "$URL/api/v1/agents" >/dev/null && break
  kill -0 "$DAEMON_PID" 2>/dev/null || fail "prismd exited during startup"
  sleep 0.2
done
curl -sf "$URL/api/v1/agents" >/dev/null || fail "prismd did not come up"

prism agents status --json > "$EV/agents-before.json" || fail "initial agents status failed"
log "initial status captured"

if [ -n "${MID:-}" ]; then
  log "MID: $MID"
  bash -c "$MID" || fail "mid-cell setup failed"
fi

wait_job() {
  local agent=$1 out=$2 state
  for _ in $(seq 1 320); do
    if prism agents job "$agent" --json > "$out"; then
      state=$(job_state "$out")
      case $state in
        succeeded|failed|unsupported|interrupted)
          log "job $agent -> $state"
          return 0
          ;;
      esac
    fi
    sleep 3
  done
  log "job $agent never reached a terminal state"
  return 1
}

inst_state=skipped
if [ -n "${AGENT:-}" ]; then
  log "install $AGENT via API"
  prism agents install "$AGENT" --json > "$EV/install-job-initial.json" \
    || fail "install POST failed"
  wait_job "$AGENT" "$EV/install-job.json" || true
  inst_state=$(job_state "$EV/install-job.json")
  prism agents status "$AGENT" --json > "$EV/status-after-install.json" || true
fi

upd_state=skipped
if [ -n "${UPDATE_AGENT:-}" ]; then
  log "update $UPDATE_AGENT via API"
  if prism agents update "$UPDATE_AGENT" --json > "$EV/update-job-initial.json"; then
    wait_job "$UPDATE_AGENT" "$EV/update-job.json" || true
    upd_state=$(job_state "$EV/update-job.json")
    prism agents status "$UPDATE_AGENT" --json > "$EV/status-after-update.json" || true
  else
    upd_state=refused
    log "update POST refused"
  fi
fi

prism agents status --json > "$EV/agents-after.json" || true
prism agents status > "$EV/agents-table.txt" 2>/dev/null || true

src=none
if [ -n "${AGENT:-${UPDATE_AGENT:-}}" ]; then
  src=$(jq -r --arg a "${AGENT:-$UPDATE_AGENT}" \
    '.agents[]? | select(.key == $a) | .source // "none"' "$EV/agents-after.json")
fi

kill "$DAEMON_PID" 2>/dev/null
wait "$DAEMON_PID" 2>/dev/null

verdict=pass
case "$inst_state" in succeeded|skipped|unsupported) ;; *) verdict=fail ;; esac
case "$upd_state" in succeeded|skipped|unsupported) ;; *) verdict=fail ;; esac
printf 'cell=%s install=%s update=%s source=%s verdict=%s\n' \
  "$CELL" "$inst_state" "$upd_state" "$src" "$verdict" > "$EV/outcome.txt"
log "cell done: install=$inst_state update=$upd_state source=$src verdict=$verdict"
[ "$verdict" = pass ]
