#!/bin/bash
set -euo pipefail

fail() {
  echo "FAIL: $1" >&2
  exit 1
}

REPO_ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)
GO_ROOT="${GO_ROOT:-$REPO_ROOT}"
PORT="${PRISM_PORT:-18787}"
CDP_PORT="${PRISM_CDP_PORT:-19222}"
HEADLESS="${PRISM_HEADLESS:-1}"
PY3="${PYTHON:-}"
if [ -z "$PY3" ]; then
  { for cand in "$(command -v python3 2>/dev/null)" /usr/bin/python3; do
      [ -n "$cand" ] || continue
      ( "$cand" -c 'import json' ) >/dev/null 2>&1 &
      if wait $!; then PY3="$cand"; break; fi
    done; } 2>/dev/null
fi
[ -n "$PY3" ] || fail "no working python3 found"
if curl -sf --max-time 3 -o /dev/null "http://127.0.0.1:$PORT/api/v1/health" 2>/dev/null; then
  fail "a daemon is already bound to port $PORT; stop it before the auth proof (the skill refuses to double-drive a shared instance)"
fi
RUNDIR=$(mktemp -d /tmp/prism-authproof.XXXXXX)
RUN_ID="$(date +%Y%m%d-%H%M%S).$$"
EVID_FINAL="${PRISM_VERIFY_EVIDENCE_DIR:-$REPO_ROOT/verify/evidence/auth-proof/$RUN_ID}"
EVID_WORK="$RUNDIR/evidence"
mkdir -p "$EVID_WORK" "$RUNDIR/.prism" "$RUNDIR/.codex" "$RUNDIR/.grok" "$RUNDIR/.omp/agent"

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

echo "==> building prismd and desktop"
(cd "$GO_ROOT" && go build -o "$RUNDIR/prismd" ./cmd/prismd) || fail "go build cmd/prismd"
npm run build --prefix "$REPO_ROOT" > "$RUNDIR/build.log" 2>&1 || fail "npm run build"
echo "==> launching isolated Electron app (isolated HOME, isolated profile)"
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
curl -sf "http://127.0.0.1:$PORT/api/v1/health" > "$EVID_WORK/health.json" || fail "daemon health failed"

for _ in $(seq 1 80); do
  curl -sf "http://127.0.0.1:$CDP_PORT/json/version" >/dev/null 2>&1 && break
  sleep 0.25
done
WS=$(node "$REPO_ROOT/verify/scripts/cdp-ws.mjs" "$CDP_PORT") || fail "no Electron CDP page target"

cdp_eval() {
  node "$REPO_ROOT/verify/scripts/cdp-eval.mjs" "$WS" "$1"
}

$PY3 - "$PORT" <<'SAFETY'
import sys, urllib.request, urllib.error, json
port = sys.argv[1]
with urllib.request.urlopen(f'http://127.0.0.1:{port}/api/v1/accounts') as r:
    accounts = json.load(r).get('accounts', [])
def auth_state(provider):
    try:
        with urllib.request.urlopen(f'http://127.0.0.1:{port}/api/v1/auth/{provider}/status') as r:
            return json.load(r).get('state')
    except urllib.error.HTTPError as e:
        if e.code in (501, 404):
            return 'unconfigured'
        raise
if accounts or auth_state('codex') == 'authorized' or auth_state('antigravity') == 'authorized':
    raise SystemExit('sandbox already has accounts or authorized sessions; refusing')
print('sandbox clean: no accounts, no authorized sessions')
SAFETY

echo "==> driving Codex add-account from the real UI"
cdp_eval 'await (async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
  const nav = [...document.querySelectorAll("nav button")].find((node) => node.textContent.includes("Accounts"))
  if (!nav) throw new Error("Accounts navigation button not found")
  nav.click()
  for (let i = 0; i < 40; i++) {
    if (document.querySelector("main h1")?.textContent.trim() === "Accounts") break
    await sleep(100)
  }
  if (document.querySelector("main h1")?.textContent.trim() !== "Accounts") throw new Error("Accounts view did not open")
  const add = [...document.querySelectorAll("main .head-actions button")].find((b) => b.textContent.trim() === "Add account")
  if (!add) throw new Error("Add account button not found")
  add.click()
  for (let i = 0; i < 40; i++) {
    if (document.querySelector("[role=dialog][aria-label=\"Add account\"]")) break
    await sleep(100)
  }
  const dialog = document.querySelector("[role=dialog][aria-label=\"Add account\"]")
  if (!dialog) throw new Error("Add account dialog not found")
  const codexSeg = [...dialog.querySelectorAll(".msm-seg-btn")].find((b) => b.textContent.trim() === "codex")
  if (!codexSeg) throw new Error("codex provider segment not found")
  codexSeg.click()
  const start = [...dialog.querySelectorAll("footer button")].find((b) => b.textContent.trim() === "Start login")
  if (!start) throw new Error("Start login button not found")
  if (start.disabled) throw new Error("Start login button disabled")
  start.click()
  for (let i = 0; i < 60; i++) {
    if (dialog.textContent.includes("Complete the login in your browser")) break
    await sleep(100)
  }
  const text = dialog.textContent
  if (!text.includes("pending")) throw new Error("Codex pending state not reached: " + text)
  if (!text.includes("Complete the login in your browser")) throw new Error("Codex pending hint missing: " + text)
  const openBrowser = [...dialog.querySelectorAll("footer button")].find((b) => b.textContent.trim() === "Open in browser")
  const copyLink = [...dialog.querySelectorAll("footer button")].find((b) => b.textContent.trim() === "Copy link")
  const cancel = [...dialog.querySelectorAll("footer button")].find((b) => b.textContent.trim() === "Cancel login")
  if (!openBrowser) throw new Error("Open browser button missing")
  if (!copyLink) throw new Error("Copy link button missing")
  if (!cancel) throw new Error("Cancel button missing")
  return {text: text.slice(0, 300)}
})()' > "$EVID_WORK/auth-codex-pending.json" || fail "Codex pending state not reached through UI"

node "$REPO_ROOT/verify/scripts/cdp-screenshot.mjs" "$WS" "$EVID_WORK/auth-codex-pending.png"

cdp_eval 'await (async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
  const dialog = document.querySelector("[role=dialog][aria-label=\"Add account\"]")
  if (!dialog) throw new Error("Add account dialog gone before cancel")
  const cancel = [...dialog.querySelectorAll("footer button")].find((b) => b.textContent.trim() === "Cancel login")
  if (!cancel) throw new Error("Cancel login button missing before cancel")
  cancel.click()
  await sleep(300)
  const start = [...dialog.querySelectorAll("footer button")].find((b) => b.textContent.trim() === "Start login")
  if (!start) throw new Error("dialog did not return to the idle Start login state after Cancel")
  return {headline: dialog.textContent.slice(0, 200)}
})()' > "$EVID_WORK/auth-codex-cancelled.json" || fail "Codex cancel path failed"

echo "==> driving Antigravity add-account from the real UI"
cdp_eval 'await (async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
  const dialog = document.querySelector("[role=dialog][aria-label=\"Add account\"]")
  if (!dialog) throw new Error("Add account dialog gone before Antigravity leg")
  const agySeg = [...dialog.querySelectorAll(".msm-seg-btn")].find((b) => b.textContent.trim() === "antigravity")
  if (!agySeg) throw new Error("antigravity provider segment not found")
  agySeg.click()
  const start = [...dialog.querySelectorAll("footer button")].find((b) => b.textContent.trim() === "Start login")
  if (!start) throw new Error("Start login button not found for Antigravity")
  if (start.disabled) throw new Error("Start login button disabled for Antigravity")
  start.click()
  for (let i = 0; i < 60; i++) {
    if (dialog.textContent.includes("Complete the login in your browser") && dialog.textContent.includes("pending")) break
    await sleep(100)
  }
  const text = dialog.textContent
  if (!text.includes("pending")) throw new Error("Antigravity pending state not reached: " + text)
  return {text: text.slice(0, 300)}
})()' > "$EVID_WORK/auth-antigravity-pending.json" || fail "Antigravity pending state not reached through UI"

node "$REPO_ROOT/verify/scripts/cdp-screenshot.mjs" "$WS" "$EVID_WORK/auth-antigravity-pending.png"

$PY3 - "$PORT" <<'SESSIONS'
import sys, urllib.request, json
port = sys.argv[1]
for provider in ('codex', 'antigravity'):
    with urllib.request.urlopen(f'http://127.0.0.1:{port}/api/v1/auth/{provider}/status') as r:
        body = json.load(r)
    print(f'{provider}: {body}')
SESSIONS

cdp_eval 'await (async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
  const dialog = document.querySelector("[role=dialog][aria-label=\"Add account\"]")
  if (!dialog) throw new Error("Add account dialog gone before Antigravity cancel")
  const cancel = [...dialog.querySelectorAll("footer button")].find((b) => b.textContent.trim() === "Cancel login")
  if (!cancel) throw new Error("Antigravity Cancel button missing before cancel")
  cancel.click()
  await sleep(300)
  const start = [...dialog.querySelectorAll("footer button")].find((b) => b.textContent.trim() === "Start login")
  if (!start) throw new Error("dialog did not return to the idle Start login state after Antigravity Cancel")
  return {headline: dialog.textContent.slice(0, 200)}
})()' > "$EVID_WORK/auth-antigravity-cancelled.json" || fail "Antigravity cancel path failed"

$PY3 - "$PORT" <<'POSTCHECK'
import sys, urllib.request, json
port = sys.argv[1]
with urllib.request.urlopen(f'http://127.0.0.1:{port}/api/v1/accounts') as r:
    accounts = json.load(r).get('accounts', [])
if accounts:
    raise SystemExit('account was created by the auth proof: ' + json.dumps(accounts))
print('no accounts created; proof never completed a login')
POSTCHECK

node "$REPO_ROOT/verify/scripts/cdp-screenshot.mjs" "$WS" "$EVID_WORK/auth-after-cancel.png"

echo "==> quitting Electron"
kill -TERM "$APP_PID"
wait "$APP_PID" 2>/dev/null || true
APP_PID=
sleep 1
if curl -sf "http://127.0.0.1:$PORT/api/v1/health" >/dev/null 2>&1; then
  fail "daemon still reachable after Electron quit"
fi

echo "auth proof passed (no login completed, no account touched)"
