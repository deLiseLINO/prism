#!/usr/bin/env bash
# Machines CRUD proof for the Prism desktop: adds a machine through the real
# form, asserts it lands in the list and the daemon config, then removes it
# through the real Remove button + confirm, and asserts the teardown.
# The proof host is "self" -> localhost, so the SSH probe succeeds for real.
set -euo pipefail

fail() { echo "FAIL: $*" >&2; exit 1; }
run_with_timeout() { perl -e 'alarm shift; exec @ARGV' "$@"; }

REPO_ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)
GO_ROOT="${GO_ROOT:-$REPO_ROOT}"
REAL_HOME=$HOME
PORT="${PRISM_PORT:-18796}"
CDP_PORT="${PRISM_CDP_PORT:-19224}"
HEADLESS="${PRISM_HEADLESS:-1}"
if curl -sf --max-time 3 -o /dev/null "http://127.0.0.1:$PORT/api/v1/health" 2>/dev/null; then
  fail "a daemon is already bound to port $PORT; stop it before verification"
fi
if ! run_with_timeout 5 ssh -o BatchMode=yes -o ConnectTimeout=3 localhost true 2>/dev/null; then
  fail "ssh localhost is not reachable; this proof needs a working key-auth loopback ssh"
fi

RUNDIR=$(mktemp -d /tmp/prism-verify.XXXXXX)
EVID_WORK="$RUNDIR/evidence"
EVID_FINAL="${PRISM_VERIFY_EVIDENCE_DIR:-/tmp/prism-verify-evidence.$(date +%Y%m%d-%H%M%S).$$}"
mkdir -p "$EVID_WORK" "$RUNDIR/.prism"

cleanup() {
  status=$?
  trap - EXIT INT TERM
  set +e
  if [ -n "${APP_PID:-}" ] && kill -0 "$APP_PID" 2>/dev/null; then
    kill -TERM "$APP_PID" 2>/dev/null
    wait "$APP_PID" 2>/dev/null
  fi
  [ -f "$RUNDIR/app.log" ] && cp "$RUNDIR/app.log" "$EVID_WORK/app.log"
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
    "hosts": {}
  }
}
CONFIG

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
curl -sf "http://127.0.0.1:$PORT/api/v1/health" > "$EVID_WORK/health.json" || fail "daemon health failed"
curl -sf "http://127.0.0.1:$PORT/api/v1/hosts" > "$EVID_WORK/hosts-before.json" || fail "hosts route failed"

for _ in $(seq 1 80); do
  curl -sf "http://127.0.0.1:$CDP_PORT/json/version" >/dev/null 2>&1 && break
  sleep 0.25
done
WS=$(node "$REPO_ROOT/verify/scripts/cdp-ws.mjs" "$CDP_PORT") || fail "no Electron CDP page target"

cdp_eval() {
  node "$REPO_ROOT/verify/scripts/cdp-eval.mjs" "$WS" "$1"
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
  const card = [...document.querySelectorAll('.cards .card')].find((node) => node.textContent.includes('Remote machines'))
  if (!card) throw new Error('remote machines flag card not rendered')
  const toggle = card.querySelector('.toggle input[type=checkbox]')
  if (!toggle) throw new Error('machines toggle not rendered')
  if (!toggle.checked) toggle.click()
  await sleep(200)
  if (!card.querySelector('.toggle input[type=checkbox]').checked) throw new Error('toggle did not switch on')
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
  const before = [...document.querySelectorAll('.int-name')].map((n) => n.textContent.trim())
  const idInput = document.querySelector('#machine-id')
  const addressInput = document.querySelector('#machine-address')
  if (!idInput || !addressInput) throw new Error('machine form inputs not rendered')
  const setNative = (el, value) => {
    const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set
    setter.call(el, value)
    el.dispatchEvent(new Event('input', {bubbles: true}))
  }
  setNative(idInput, 'self')
  setNative(addressInput, 'localhost')
  for (let i = 0; i < 40; i++) {
    const btn = [...document.querySelectorAll('button')].find((n) => n.textContent.trim() === 'Add machine')
    if (btn && !btn.disabled) { btn.click(); break }
    await sleep(100)
  }
  await sleep(3000)
  const rows = [...document.querySelectorAll('.int-name')].map((n) => n.textContent.trim())
  return {before, rows, body: document.querySelector('main')?.innerText}
})()" > "$EVID_WORK/machine-add.json" || fail "adding a machine through the form failed"

grep -q '"self"' "$EVID_WORK/machine-add.json" || fail "added machine does not appear in the list"

echo "==> cross-checking the daemon state"
curl -sf "http://127.0.0.1:$PORT/api/v1/hosts" > "$EVID_WORK/hosts-after-add.json" || fail "hosts route failed after add"
grep -q '"id":"self"' "$EVID_WORK/hosts-after-add.json" || fail "daemon does not list the added host"
grep -q '"status":"ok"' "$EVID_WORK/hosts-after-add.json" || fail "added host did not resolve (expected ok for localhost)"
grep -q '"self":' "$RUNDIR/.prism/prism.json" || fail "host not persisted in the daemon config file"
grep -q '"address": "localhost"' "$RUNDIR/.prism/prism.json" || fail "host address not persisted in the daemon config file"

node "$REPO_ROOT/verify/scripts/cdp-screenshot.mjs" "$WS" "$EVID_WORK/machines-added.png"

echo "==> removing the machine through the real UI"
cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
  const row = [...document.querySelectorAll('.int-row-wrap')].find((n) => n.querySelector('.int-name')?.textContent.trim() === 'self')
  if (!row) throw new Error('self row not found')
  const removeBtn = [...row.querySelectorAll('button')].find((n) => n.textContent.trim() === 'Remove')
  if (!removeBtn) throw new Error('Remove button not found')
  removeBtn.click()
  for (let i = 0; i < 40; i++) {
    if (row.querySelector('.confirm')) break
    await sleep(100)
  }
  const confirm = row.querySelector('.confirm')
  if (!confirm) throw new Error('confirm dialog did not render')
  const confirmBtn = [...confirm.querySelectorAll('button')].find((n) => n.textContent.trim() === 'Remove')
  if (!confirmBtn) throw new Error('confirm Remove button not found')
  confirmBtn.click()
  for (let i = 0; i < 60; i++) {
    await sleep(200)
    const names = [...document.querySelectorAll('.int-name')].map((n) => n.textContent.trim())
    if (!names.includes('self')) break
  }
  const names = [...document.querySelectorAll('.int-name')].map((n) => n.textContent.trim())
  if (names.includes('self')) throw new Error('self row still listed after removal')
  return {names, body: document.querySelector('main')?.innerText}
})()" > "$EVID_WORK/machine-remove.json" || fail "removing the machine through the UI failed"

curl -sf "http://127.0.0.1:$PORT/api/v1/hosts" > "$EVID_WORK/hosts-after-remove.json" || fail "hosts route failed after remove"
if grep -q '"id":"self"' "$EVID_WORK/hosts-after-remove.json"; then
  fail "daemon still lists the removed host"
fi
if grep -q '"self"' "$RUNDIR/.prism/prism.json"; then
  fail "removed host still present in the daemon config file"
fi
node "$REPO_ROOT/verify/scripts/cdp-screenshot.mjs" "$WS" "$EVID_WORK/machines-removed.png"

echo "VERIFIED machines add and remove in the real UI"
