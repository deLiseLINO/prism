#!/bin/bash

agents_sandbox() {
  [ "$(uname -s)" = Darwin ] || fail "client maintenance driver requires the macOS filesystem sandbox"
  [ -x /usr/bin/sandbox-exec ] || fail "sandbox-exec is required"
  case "$RUNDIR$REPO_ROOT" in *'"'*|*'\\'*) fail "unsupported sandbox path";; esac
  SANDBOX_PROFILE="$RUNDIR/maintenance.sb"
  cat > "$SANDBOX_PROFILE" <<POLICY
(version 1)
(allow default)
(deny file-read* (subpath "/opt/homebrew") (subpath "/usr/local"))
(deny process-exec (require-not (require-any (subpath "$RUNDIR") (subpath "/bin") (subpath "/usr/bin") (subpath "$REPO_ROOT/node_modules/electron"))))
(deny file-write* (require-not (subpath "$RUNDIR")))
(deny network-outbound (require-not (remote ip "localhost:*")))
POLICY
  CLIENT_ENV=(
    "CLAUDE_CODE_EXECUTABLE=$NPM_PREFIX/bin/claude"
    "CODEX_EXECUTABLE=$NPM_PREFIX/bin/codex"
    "OMP_EXECUTABLE=$NPM_PREFIX/bin/omp"
    "PI_EXECUTABLE=$NPM_PREFIX/bin/pi"
    "OPENCODE_EXECUTABLE=$NPM_PREFIX/bin/opencode"
    "HERMES_EXECUTABLE=$NPM_PREFIX/bin/hermes"
    "GROK_EXECUTABLE=$RUNDIR/sandbox/home/.grok/bin/grok"
  )
  /usr/bin/sandbox-exec -f "$SANDBOX_PROFILE" /bin/sh -c 'test ! -r /opt/homebrew/bin/brew && test ! -r /usr/local/bin/brew' || fail "fixed installer directories are not isolated"
  /usr/bin/sandbox-exec -f "$SANDBOX_PROFILE" /bin/sh -c 'printf forbidden > "$1"' sh "$RUNDIR/../prism-forbidden-$$" 2>/dev/null && fail "filesystem writes escaped the sandbox"
  return 0
}
