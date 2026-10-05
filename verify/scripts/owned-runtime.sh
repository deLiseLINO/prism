#!/usr/bin/env bash
_VERIFY_HELPER="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)/owned-runtime.py"
_VERIFY_PYTHON="$(command -v python3)"
_VERIFY_NODE="$(command -v node || :)"

_verify_boundary() {
  "$_VERIFY_PYTHON" "$_VERIFY_HELPER" "$@"
}

_verify_checked() {
  if _verify_boundary "$@"; then return 0; fi
  _VERIFY_FAILED=1
  return 1
}

_verify_exit() {
  local status=$?
  set +e
  verify_finish "$status"
  exit "$?"
}

verify_init() {
  [ "${_VERIFY_INITIALIZED:-0}" = 0 ] || return 1
  local name=$1
  case "$name" in ''|*[!a-zA-Z0-9_-]*) return 1 ;; esac
  PORT=$2
  CDP_PORT=${3:-}
  RUNDIR=$(mktemp -d "/tmp/prism-verify-${name}.XXXXXXXX") || return 1
  RUNDIR=$(cd "$RUNDIR" && pwd -P) || { printf 'owned runtime: private sandbox retained: %s\n' "$RUNDIR" >&2; return 1; }
  VERIFY_HOME=$RUNDIR
  EVID_WORK="$RUNDIR/evidence"
  EVID_FINAL=${PRISM_VERIFY_EVIDENCE_DIR:-"${RUNDIR}-evidence"}
  DAEMON_LOG="$RUNDIR/.prism/prism.log"
  RUN_ID=${RUN_ID:-"$(date +%Y%m%d-%H%M%S).$$"}
  APP_PID=
  WS=
  _VERIFY_INITIALIZED=1
  _VERIFY_FAILED=0
  _VERIFY_FINISHED=0
  _VERIFY_RESULT=1
  trap _verify_exit EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  _verify_checked init "$RUNDIR" "$PORT" "$CDP_PORT"
}

_verify_prepare() {
  [ "${_VERIFY_INITIALIZED:-0}" = 1 ] && [ "${_VERIFY_FINISHED:-0}" = 0 ] || return 1
  VERIFY_PATH=${VERIFY_PATH:-$PATH}
  VERIFY_SHELL=${VERIFY_SHELL:-"$RUNDIR/login-shell"}
  if ! _verify_checked environment "$RUNDIR" "$PORT" "$VERIFY_PATH" "$VERIFY_SHELL" "${HEADLESS:-1}" > "$RUNDIR/.runtime/environment.args"; then return 1; fi
  _VERIFY_ENV=()
  local item
  while IFS= read -r -d '' item; do _VERIFY_ENV+=("$item"); done < "$RUNDIR/.runtime/environment.args"
  _verify_checked prepare "$RUNDIR" "$PORT" "$REPO_ROOT" "$CDP_PORT" "${GO_ROOT:-$REPO_ROOT}"
}

verify_stage_state() {
  _verify_checked stage "$RUNDIR" "$PORT" "$1" || return 1
  VERIFY_LIVE=1
}

verify_start_electron() {
  [ -n "$CDP_PORT" ] && [ -n "$_VERIFY_NODE" ] || { _VERIFY_FAILED=1; return 1; }
  _verify_prepare || return 1
  local executable deadline
  executable=$(_verify_boundary electron "$RUNDIR" "$PORT" "$REPO_ROOT") || { _VERIFY_FAILED=1; return 1; }
  (
    cd "$REPO_ROOT/apps/desktop" || exit 1
    exec /usr/bin/env -i "${_VERIFY_ENV[@]}" "$executable" . "--remote-debugging-port=$CDP_PORT" "--user-data-dir=$RUNDIR/electron"
  ) > "$RUNDIR/app.log" 2>&1 &
  APP_PID=$!
  _verify_checked capture "$RUNDIR" "$PORT" "$APP_PID" "$executable" || return 1
  _verify_checked ready "$RUNDIR" "$PORT" || return 1
  deadline=$((SECONDS + 45))
  while [ "$SECONDS" -lt "$deadline" ]; do
    if WS=$(_verify_boundary ws "$RUNDIR" "$PORT" "$CDP_PORT" "$REPO_ROOT" "$_VERIFY_NODE" 2> "$RUNDIR/.runtime/cdp-error.log"); then return 0; fi
    sleep 0.2
  done
  printf '%s\n' 'owned runtime: renderer readiness timed out' >&2
  _VERIFY_FAILED=1
  return 1
}

verify_start_daemon() {
  _verify_prepare || return 1
  _verify_checked start "$RUNDIR" "$PORT"
}

verify_attest_daemon() {
  _verify_checked attest "$RUNDIR" "$PORT"
}

verify_crash_daemon() {
  _verify_checked crash "$RUNDIR" "$PORT"
}

verify_quit_app() {
  [ -n "${APP_PID:-}" ] || return 0
  _verify_checked quit "$RUNDIR" "$PORT" "$CDP_PORT" || return 1
  if wait "$APP_PID"; then :; else :; fi
}

verify_stop_daemon() {
  if [ -n "${APP_PID:-}" ] && [ ! -f "$RUNDIR/.runtime/app.json" ]; then
    printf '%s\n' 'owned runtime: supervisor identity unavailable; refusing service stop' >&2
    _VERIFY_FAILED=1
    return 1
  fi
  _verify_checked stop "$RUNDIR" "$PORT"
}

verify_finish() {
  [ "${_VERIFY_INITIALIZED:-0}" = 1 ] || return "${1:-1}"
  [ "${_VERIFY_FINISHED:-0}" = 0 ] || return "$_VERIFY_RESULT"
  _VERIFY_FINISHED=1
  trap - EXIT
  trap '' INT TERM
  local status=${1:-1} cleanup=${_VERIFY_FAILED:-0} app_stopped=0 service_stopped=0
  if verify_quit_app; then app_stopped=1; else cleanup=1; fi
  if [ "$app_stopped" = 1 ]; then
    if verify_stop_daemon; then service_stopped=1; else cleanup=1; fi
  fi
  if [ "$app_stopped" = 1 ] && [ "$service_stopped" = 1 ] && [ "${VERIFY_LIVE:-0}" != 1 ]; then
    if ! _verify_boundary logs "$RUNDIR" "$PORT"; then cleanup=1; fi
  fi
  if ! _verify_boundary finalize "$RUNDIR" "$PORT" "$EVID_FINAL" "${VERIFY_LIVE:-0}" "$status" "$cleanup" "$app_stopped" "$service_stopped"; then cleanup=1; fi
  _VERIFY_RESULT=$status
  if [ "$cleanup" != 0 ] || [ "$status" != 0 ]; then
    if [ -d "$RUNDIR" ]; then printf 'owned runtime: private sandbox retained: %s\n' "$RUNDIR" >&2; fi
    [ "$_VERIFY_RESULT" != 0 ] || _VERIFY_RESULT=1
  fi
  trap - INT TERM
  return "$_VERIFY_RESULT"
}
