#!/usr/bin/env bash
# Remote daemon install proof for the Prism desktop. Adds a host pointing at
# loopback ssh WITHOUT a daemon port, then drives the real UI: the Install
# prismd button streams the bundled binary over ssh, starts it, flips the host
# to external, and Manage routes through the freshly installed daemon.
set -euo pipefail

fail() { echo "FAIL: $*" >&2; exit 1; }
run_with_timeout() { perl -e 'alarm shift; exec @ARGV' "$@"; }

REPO_ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)
GO_ROOT="${GO_ROOT:-$REPO_ROOT}"
PORT="${PRISM_PORT:-18801}"
REMOTE_PORT="${PRISM_REMOTE_DAEMON_PORT:-18802}"
CDP_PORT="${PRISM_CDP_PORT:-19301}"
HEADLESS="${PRISM_HEADLESS:-1}"
for probe_port in "$PORT" "$REMOTE_PORT"; do
  if curl -sf --max-time 3 -o /dev/null "http://127.0.0.1:$probe_port/api/v1/health" 2>/dev/null; then
    fail "a daemon is already bound to port $probe_port; stop it before verification"
  fi
done
if ! run_with_timeout 5 ssh -o BatchMode=yes -o ConnectTimeout=3 localhost true 2>/dev/null; then
  fail "ssh localhost is not reachable; this proof needs a working key-auth loopback ssh"
fi

RUNDIR=$(mktemp -d /tmp/prism-install-verify.XXXXXX)
EVID_WORK="$RUNDIR/evidence"
EVID_FINAL="${PRISM_VERIFY_EVIDENCE_DIR:-/tmp/prism-verify-evidence.$(date +%Y%m%d-%H%M%S).$$}"
mkdir -p "$EVID_WORK" "$EVID_FINAL" "$RUNDIR/.prism"

cleanup() {
  status=$?
  trap - EXIT INT TERM
  set +e
  if [ -n "${APP_PID:-}" ] && kill -0 "$APP_PID" 2>/dev/null; then
    kill -TERM "$APP_PID" 2>/dev/null
    wait "$APP_PID" 2>/dev/null
  fi
  INSTALLED_PID=$(pgrep -f "prismd.*--listen 127.0.0.1:$REMOTE_PORT" | head -1 || true)
  [ -n "$INSTALLED_PID" ] && kill -TERM "$INSTALLED_PID" 2>/dev/null
  ssh -o BatchMode=yes localhost "rm -rf ~/.prism/remote" 2>/dev/null
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
    "hosts": {
      "self": {"address": "localhost"}
    }
  }
}
CONFIG

echo "==> building prismd and desktop"
(cd "$GO_ROOT" && go build -o "$RUNDIR/prismd" ./cmd/prismd) || fail "go build cmd/prismd"
npm run build --prefix "$REPO_ROOT" > "$RUNDIR/build.log" 2>&1 || fail "npm run build"

echo "==> launching isolated Electron app (remote daemon NOT pre-started)"
PRISMD_PATH="$RUNDIR/prismd" \
PRISM_PORT="$PORT" \
PRISM_DAEMON_CONFIG="$RUNDIR/.prism/prism.json" \
PRISM_REMOTE_DAEMON_PORT="$REMOTE_PORT" \
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
curl -sf "http://127.0.0.1:$PORT/api/v1/hosts" > "$EVID_WORK/hosts-before.json" || fail "local daemon did not come up"

WS=""
for _ in $(seq 1 80); do
  WS=$(node "$REPO_ROOT/verify/scripts/cdp-ws.mjs" "$CDP_PORT" 2>/dev/null) && break
  sleep 0.5
done
[ -n "$WS" ] || fail "no CDP websocket"

cdp_eval() {
  node "$REPO_ROOT/verify/scripts/cdp-eval.mjs" "$WS" "$1" "${2:-30000}"
}

echo "==> the Machines tab and install button stay hidden while the flag is off"
cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
  const navBtn = (l) => [...document.querySelectorAll('nav button')].find((b) => b.textContent.trim() === l)
  if (navBtn('Machines')) throw new Error('Machines tab rendered in the nav while the flag is off')
  window.location.hash = '#/machines'
  await sleep(300)
  if (document.querySelector('main h1')?.textContent.trim() === 'Machines') throw new Error('direct #/machines hash rendered the view while the flag is off')
  return {hidden: true}
})()" > "$EVID_WORK/install-hidden.json" || { cat "$EVID_WORK"/install-hidden.json 2>/dev/null || true; fail "Machines not gated by the experimental flag"; }

grep -q '"hidden":true' "$EVID_WORK/install-hidden.json" || fail "install-hidden evidence missing hidden true"

echo "==> enabling the experimental remote install flag"
cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
  const nav = (l) => [...document.querySelectorAll('nav button')].find((b) => b.textContent.trim() === l)
  nav('Experimental').click()
  for (let i = 0; i < 60; i++) { if (document.querySelector('main h1')?.textContent.trim() === 'Experimental') break; await sleep(100) }
  if (document.querySelector('main h1')?.textContent.trim() !== 'Experimental') throw new Error('Experimental view did not open')
  const card = [...document.querySelectorAll('.cards .card')].find((node) => node.textContent.includes('Remote machines'))
  if (!card) throw new Error('remote machines flag card not rendered')
  const toggle = card.querySelector('.toggle input[type=checkbox]')
  if (!toggle) throw new Error('remote install toggle not rendered')
  if (!toggle.checked) toggle.click()
  await sleep(200)
  if (!card.querySelector('.toggle input[type=checkbox]').checked) throw new Error('toggle did not switch on')
  return {enabled: true}
})()" > "$EVID_WORK/flag-enable.json" || { cat "$EVID_WORK"/flag-enable.json 2>/dev/null || true; fail "experimental flag toggle failed"; }

grep -q '"enabled":true' "$EVID_WORK/flag-enable.json" || fail "flag toggle evidence missing enabled true"

echo "==> driving the Machines UI: Install prismd on the self host"
cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
  const nav = (l) => [...document.querySelectorAll('nav button')].find((b) => b.textContent.trim() === l)
  nav('Machines').click()
  for (let i = 0; i < 60; i++) { if (document.querySelector('main h1')?.textContent.trim() === 'Machines') break; await sleep(100) }
  if (document.querySelector('main h1')?.textContent.trim() !== 'Machines') throw new Error('Machines view did not open')
  const row = [...document.querySelectorAll('.int-row-wrap')].find((n) => n.querySelector('.int-name')?.textContent.trim() === 'self')
  if (!row) throw new Error('self host row not rendered')
  const install = [...row.querySelectorAll('button')].find((b) => /install prismd/i.test(b.textContent))
  if (!install) throw new Error('Install prismd button not rendered with the flag on and no daemon port')
  install.click()
  await sleep(500)
  return {rowText: row.innerText.replace(/\n/g, ' | ')}
})()" > "$EVID_WORK/install-click.json" || { cat "$EVID_WORK"/install-click.json 2>/dev/null || true; fail "install click failed in the UI"; }

grep -qE 'Install prismd|Installing' "$EVID_WORK/install-click.json" || fail "install click evidence missing the button"

echo "==> switching tabs mid-install keeps the busy state, no already-running error"
cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
  const nav = (l) => [...document.querySelectorAll('nav button')].find((b) => b.textContent.trim() === l)
  nav('Overview').click()
  for (let i = 0; i < 60; i++) { if (document.querySelector('main h1')?.textContent.trim() === 'Overview') break; await sleep(100) }
  if (document.querySelector('main h1')?.textContent.trim() !== 'Overview') throw new Error('Overview view did not open mid-install')
  nav('Machines').click()
  for (let i = 0; i < 60; i++) { if (document.querySelector('main h1')?.textContent.trim() === 'Machines') break; await sleep(100) }
  if (document.querySelector('main h1')?.textContent.trim() !== 'Machines') throw new Error('Machines view did not reopen mid-install')
  const row = [...document.querySelectorAll('.int-row-wrap')].find((n) => n.querySelector('.int-name')?.textContent.trim() === 'self')
  if (!row) throw new Error('self row not rendered after tab switch')
  const refusal = row.querySelector('.int-refusal__msg')?.textContent ?? ''
  if (/already running/i.test(refusal)) throw new Error('already-running error surfaced after a tab switch: ' + refusal)
  const install = [...row.querySelectorAll('button')].find((b) => /installing|install prismd/i.test(b.textContent))
  if (!install) return {settled: true, rowText: row.innerText.replace(/\n/g, ' | ')}
  if (!/installing/i.test(install.textContent)) throw new Error('install button lost its busy state after a tab switch: ' + install.textContent)
  return {busyAfterSwitch: true, rowText: row.innerText.replace(/\n/g, ' | ')}
})()" 60000 > "$EVID_WORK/tab-switch.json" || { cat "$EVID_WORK"/tab-switch.json 2>/dev/null || true; fail "tab switch mid-install broke the state"; }

grep -qE '"busyAfterSwitch":true|"settled":true' "$EVID_WORK/tab-switch.json" || fail "tab-switch evidence missing the state"

echo "==> waiting for the install to finish (upload + start + verify)"
cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
  for (let i = 0; i < 120; i++) {
    const row = [...document.querySelectorAll('.int-row-wrap')].find((n) => n.querySelector('.int-name')?.textContent.trim() === 'self')
    if (!row) throw new Error('self row vanished during install')
    const refusal = row.querySelector('.int-refusal__msg')?.textContent ?? ''
    if (refusal !== '') throw new Error('install failed: ' + refusal)
    const install = [...row.querySelectorAll('button')].find((b) => /installing|install prismd|retry/i.test(b.textContent))
    if (!install) return {done: true, rowText: row.innerText.replace(/\n/g, ' | ')}
    await sleep(1000)
  }
  throw new Error('install button still present after 120s')
})()" 150000 > "$EVID_WORK/install-wait.json" || { cat "$EVID_WORK"/install-wait.json 2>/dev/null || true; fail "install did not converge in the UI"; }

grep -q '"done":true' "$EVID_WORK/install-wait.json" || fail "install wait did not record completion"


echo "==> the host flipped to external: daemon port saved, Manage enabled"
curl -sf "http://127.0.0.1:$PORT/api/v1/hosts" > "$EVID_WORK/hosts-after.json" || fail "hosts list after install"
grep -q "\"daemonPort\":$REMOTE_PORT" "$EVID_WORK/hosts-after.json" || fail "host self did not flip to its own daemon port"

curl -sf "http://127.0.0.1:$REMOTE_PORT/api/v1/health" > "$EVID_WORK/installed-health.json" || fail "the installed prismd is not answering on $REMOTE_PORT"
grep -q '"status":"ok"' "$EVID_WORK/installed-health.json" || fail "installed prismd health is not ok"
ssh -o BatchMode=yes localhost "test -x ~/.prism/remote/prismd && tail -c 2000 ~/.prism/remote/prismd.log" > "$EVID_WORK/installed-prismd.log" 2>/dev/null || true
ssh -o BatchMode=yes localhost "test -x ~/.prism/remote/prismd" || fail "prismd binary was not placed in ~/.prism/remote on the ssh target"


echo "==> Manage routes through the installed daemon"
cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
  const row = [...document.querySelectorAll('.int-row-wrap')].find((n) => n.querySelector('.int-name')?.textContent.trim() === 'self')
  const manage = [...row.querySelectorAll('button')].find((b) => /^manage$/i.test(b.textContent.trim()))
  if (!manage || manage.disabled) throw new Error('Manage button not enabled after install')
  manage.click()
  for (let i = 0; i < 60; i++) {
    if (/managing self/i.test(document.body.innerText)) break
    await sleep(500)
  }
  if (!/managing self/i.test(document.body.innerText)) throw new Error('banner did not switch to Managing self')
  await sleep(3000)
  const nav = (l) => [...document.querySelectorAll('nav button')].find((b) => b.textContent.trim() === l)
  nav('Providers').click()
  await sleep(2500)
  const main = document.querySelector('main')?.innerText ?? ''
  if (/loading/i.test(main)) throw new Error('Providers still loading on the installed daemon')
  if (!/codex/i.test(main) && !/no providers/i.test(main)) throw new Error('Providers content unexpected: ' + main.slice(0, 120))
  return {banner: /managing self/i.test(document.body.innerText), providersSnippet: main.slice(0, 100)}
})()" 60000 > "$EVID_WORK/manage-installed.json" || { cat "$EVID_WORK"/manage-installed.json 2>/dev/null || true; fail "Manage did not route through the installed daemon"; }

grep -q '"banner":true' "$EVID_WORK/manage-installed.json" || fail "manage evidence missing banner switch"

echo "VERIFIED remote daemon install in the real UI"
