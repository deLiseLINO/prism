#!/usr/bin/env bash
# Remote daemon switching proof for the Prism desktop. Launches two prismd
# instances (the app's local one and a "remote" one reached over the loopback
# ssh forward), seeds a host with daemonPort, then drives the real UI:
# Manage switches every view to the remote daemon; the banner shows the remote
# host; Back to this machine restores local data.
set -euo pipefail

fail() { echo "FAIL: $*" >&2; exit 1; }
run_with_timeout() { perl -e 'alarm shift; exec @ARGV' "$@"; }

REPO_ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd -P)
GO_ROOT="${GO_ROOT:-$REPO_ROOT}"
PORT="${PRISM_PORT:-18796}"
REMOTE_PORT="${PRISM_REMOTE_PORT:-18797}"
CDP_PORT="${PRISM_CDP_PORT:-19224}"
HEADLESS="${PRISM_HEADLESS:-1}"
for probe_port in "$PORT" "$REMOTE_PORT"; do
  if curl -sf --max-time 3 -o /dev/null "http://127.0.0.1:$probe_port/api/v1/health" 2>/dev/null; then
    fail "a daemon is already bound to port $probe_port; stop it before verification"
  fi
done
if ! run_with_timeout 5 ssh -o BatchMode=yes -o ConnectTimeout=3 localhost true 2>/dev/null; then
  fail "ssh localhost is not reachable; this proof needs a working key-auth loopback ssh"
fi

RUNDIR=$(mktemp -d /tmp/prism-verify.XXXXXX)
EVID_WORK="$RUNDIR/evidence"
EVID_FINAL="${PRISM_VERIFY_EVIDENCE_DIR:-/tmp/prism-verify-evidence.$(date +%Y%m%d-%H%M%S).$$}"
mkdir -p "$EVID_WORK" "$RUNDIR/.prism" "$RUNDIR/.remote"

cleanup() {
  status=$?
  trap - EXIT INT TERM
  set +e
  if [ -n "${APP_PID:-}" ] && kill -0 "$APP_PID" 2>/dev/null; then
    kill -TERM "$APP_PID" 2>/dev/null
    wait "$APP_PID" 2>/dev/null
  fi
  if [ -n "${REMOTE_PID:-}" ] && kill -0 "$REMOTE_PID" 2>/dev/null; then
    kill -TERM "$REMOTE_PID" 2>/dev/null
    wait "$REMOTE_PID" 2>/dev/null
  fi
  [ -f "$RUNDIR/app.log" ] && cp "$RUNDIR/app.log" "$EVID_WORK/app.log"
  [ -f "$RUNDIR/remote.log" ] && cp "$RUNDIR/remote.log" "$EVID_WORK/remote.log"
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
      "codex": {"wire": "codex", "models": ["gpt-5.6-luna"]}
    },
    "combos": {},
    "routes": {},
    "aliases": {},
    "hosts": {
      "self": {"address": "localhost", "daemonPort": $REMOTE_PORT}
    }
  }
}
CONFIG

cat > "$RUNDIR/.remote/prism.json" <<CONFIG
{
  "version": 1,
  "generation": 0,
  "config": {
    "version": 1,
    "daemon": {"listen": "127.0.0.1:$REMOTE_PORT"},
    "providers": {
      "antigravity": {"wire": "antigravity", "models": ["remote-only-model"]}
    },
    "combos": {},
    "routes": {},
    "aliases": {},
    "hosts": {}
  }
}
CONFIG

echo "==> building prismd and desktop"
(cd "$GO_ROOT" && go build -o "$RUNDIR/prismd" ./cmd/prismd) || fail "go build cmd/prismd"
npm run build --prefix "$REPO_ROOT" > "$RUNDIR/build.log" 2>&1 || fail "npm run build"

echo "==> launching the remote daemon (loopback stand-in for the remote machine)"

HOME="$RUNDIR" "$RUNDIR/prismd" \
  --listen "127.0.0.1:$REMOTE_PORT" \
  --config "$RUNDIR/.remote/prism.json" \
  --credential-store "$RUNDIR/.remote" > "$RUNDIR/remote.log" 2>&1 &
REMOTE_PID=$!

for _ in $(seq 1 60); do
  curl -sf --max-time 1 -o /dev/null "http://127.0.0.1:$REMOTE_PORT/api/v1/health" 2>/dev/null && break
  sleep 0.5
done
curl -sf "http://127.0.0.1:$REMOTE_PORT/api/v1/providers" > "$EVID_WORK/remote-providers.json" || fail "remote daemon did not come up"

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
  curl -sf --max-time 1 -o /dev/null "http://127.0.0.1:$PORT/api/v1/health" 2>/dev/null && break
  sleep 0.5
done
curl -sf "http://127.0.0.1:$PORT/api/v1/health" > "$EVID_WORK/health.json" || fail "local daemon health failed"

for _ in $(seq 1 80); do
  WS=$(node "$REPO_ROOT/scripts/verify/scripts/cdp-ws.mjs" "$CDP_PORT" 2>/dev/null) && break
  sleep 0.5
done
[ -n "${WS:-}" ] || fail "no Electron CDP page target"

cdp_eval() {
  node "$REPO_ROOT/scripts/verify/scripts/cdp-eval.mjs" "$WS" "$1" 30000
}

echo "==> enabling the experimental machines flag"
cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
  const nav = [...document.querySelectorAll('nav button')].find((node) => node.textContent.includes('Experimental'))
  if (!nav) throw new Error('Experimental navigation button not found')
  nav.click()
  for (let i = 0; i < 40; i++) {
    if (document.querySelector('main h1')?.textContent.trim() === 'Experimental') break
    await sleep(100)
  }
  if (document.querySelector('main h1')?.textContent.trim() !== 'Experimental') throw new Error('Experimental view did not open')
  const toggle = document.querySelector('.toggle input[type=checkbox]')
  if (!toggle) throw new Error('machines toggle not rendered')
  if (!toggle.checked) toggle.click()
  await sleep(200)
  if (!document.querySelector('.toggle input[type=checkbox]').checked) throw new Error('toggle did not switch on')
  return {enabled: true}
})()" > "$EVID_WORK/flag-enable.json" || fail "enabling the machines flag failed"

cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
  const nav = [...document.querySelectorAll('nav button')].find((node) => node.textContent.includes('Machines'))
  if (!nav) throw new Error('Machines navigation button not found')
  nav.click()
  for (let i = 0; i < 40; i++) {
    if (document.querySelector('main h1')?.textContent.trim() === 'Machines') break
    await sleep(100)
  }
  if (document.querySelector('main h1')?.textContent.trim() !== 'Machines') throw new Error('Machines view did not open')
  const selfRow = [...document.querySelectorAll('.int-row-wrap')].find((n) => n.querySelector('.int-name')?.textContent.trim() === 'self')
  if (!selfRow) throw new Error('self row not found; is daemonPort missing from the seeded host?')
  const manageBtn = [...selfRow.querySelectorAll('button')].find((n) => n.textContent.trim() === 'Manage')
  if (!manageBtn) throw new Error('Manage button not found on the self row')
  if (manageBtn.disabled) throw new Error('Manage button disabled; host status should be ok')
  manageBtn.click()
  await sleep(5000)
  const banner = document.querySelector('.banner__title')?.textContent.trim() ?? ''
  return {banner, body: document.querySelector('main')?.innerText}
})()" > "$EVID_WORK/manage-switch.json" || fail "switching to the remote machine failed"

grep -q 'Managing self' "$EVID_WORK/manage-switch.json" || fail "remote banner did not appear after Manage"

echo "==> asserting the Providers view renders the remote daemon's data"
cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
  const nav = [...document.querySelectorAll('nav button')].find((node) => node.textContent.includes('Providers'))
  if (!nav) throw new Error('Providers navigation button not found')
  nav.click()
  for (let i = 0; i < 60; i++) {
    const text = document.querySelector('main')?.innerText ?? ''
    if (text.includes('antigravity') && text.includes('remote-only-model')) break
    await sleep(400)
  }

  const text = document.querySelector('main')?.innerText ?? ''
  if (!text.includes('antigravity')) throw new Error('remote provider antigravity not shown in Providers view')
  if (!text.includes('remote-only-model')) throw new Error('remote-only-model not shown in Providers view')
  if (text.includes('gpt-5.6-luna')) throw new Error('local model gpt-5.6-luna leaked into the remote view')
  return {text}
})()" > "$EVID_WORK/remote-providers.json" || fail "Providers view did not switch to the remote daemon"

node "$REPO_ROOT/scripts/verify/scripts/cdp-screenshot.mjs" "$WS" "$EVID_WORK/remote-providers.png"

echo "==> switching back to this machine"
cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
  const back = [...document.querySelectorAll('.banner__action button')].find((n) => n.textContent.trim() === 'Back to this machine')
  if (!back) throw new Error('Back to this machine button not found')
  back.click()
  await sleep(2000)
  const nav = [...document.querySelectorAll('nav button')].find((node) => node.textContent.includes('Providers'))
  if (!nav) throw new Error('Providers navigation button not found')
  nav.click()
  for (let i = 0; i < 60; i++) {
    const text = document.querySelector('main')?.innerText ?? ''
    if (text.includes('gpt-5.6-luna') && !text.includes('antigravity')) break
    await sleep(400)
  }

  const text = document.querySelector('main')?.innerText ?? ''
  if (!text.includes('gpt-5.6-luna')) throw new Error('local provider not restored after switching back')
  if (text.includes('antigravity')) throw new Error('remote provider still shown after switching back')
  if (document.body.innerText.includes('Managing self')) throw new Error('remote banner still visible after switching back')
  return {text}
})()" > "$EVID_WORK/back-to-local.json" || fail "switching back to the local machine failed"

node "$REPO_ROOT/scripts/verify/scripts/cdp-screenshot.mjs" "$WS" "$EVID_WORK/local-providers.png"


echo "VERIFIED remote daemon switching in the real UI"
