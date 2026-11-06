#!/usr/bin/env bash
# Remote daemon install proof for the Prism desktop. Adds a machine pointing at
# loopback ssh through the real Machines form, then asserts the install starts
# on its own: the bundled binary is streamed over ssh and started, the host
# gets its daemon port, auto sync is on and pushes this machine's config to the
# new daemon, and Manage routes through the freshly installed daemon.
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
  INSTALLED_PID=$(pgrep -f "prism.* daemon --listen 127.0.0.1:$REMOTE_PORT" | head -1 || true)
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
    "hosts": {}
  }
}
CONFIG

echo "==> building prism and desktop"
(cd "$GO_ROOT" && go build -o "$RUNDIR/prism" ./cmd/prism) || fail "go build cmd/prism"
npm run build --prefix "$REPO_ROOT" > "$RUNDIR/build.log" 2>&1 || fail "npm run build"

echo "==> launching isolated Electron app (remote daemon NOT pre-started)"
PRISMD_PATH="$RUNDIR/prism" \
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
  const card = [...document.querySelectorAll('.experimental-flag')].find((node) => node.textContent.includes('Remote machines'))
  if (!card) throw new Error('remote machines flag card not rendered')
  const toggle = card.querySelector('input[type=checkbox]')
  if (!toggle) throw new Error('remote install toggle not rendered')
  if (!toggle.checked) toggle.click()
  await sleep(200)
  if (!card.querySelector('input[type=checkbox]').checked) throw new Error('toggle did not switch on')
  return {enabled: true}
})()" > "$EVID_WORK/flag-enable.json" || { cat "$EVID_WORK"/flag-enable.json 2>/dev/null || true; fail "experimental flag toggle failed"; }

grep -q '"enabled":true' "$EVID_WORK/flag-enable.json" || fail "flag toggle evidence missing enabled true"

echo "==> adding the self machine through the form starts the install on its own"
cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
  const nav = (l) => [...document.querySelectorAll('nav button')].find((b) => b.textContent.trim() === l)
  nav('Machines').click()
  for (let i = 0; i < 60; i++) { if (document.querySelector('main h1')?.textContent.trim() === 'Machines') break; await sleep(100) }
  if (document.querySelector('main h1')?.textContent.trim() !== 'Machines') throw new Error('Machines view did not open')
  const setNative = (el, value) => {
    const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set
    setter.call(el, value)
    el.dispatchEvent(new Event('input', {bubbles: true}))
  }
  setNative(document.querySelector('#machine-id'), 'self')
  setNative(document.querySelector('#machine-address'), 'localhost')
  for (let i = 0; i < 40; i++) {
    const btn = [...document.querySelectorAll('button')].find((n) => n.textContent.trim() === 'Add machine')
    if (btn && !btn.disabled) { btn.click(); break }
    await sleep(100)
  }
  let row = null
  for (let i = 0; i < 50; i++) {
    row = [...document.querySelectorAll('.mach-item')].find((n) => n.querySelector('.lrow__title > span:first-child')?.textContent.trim() === 'self')
    if (row && /installing/i.test(row.innerText)) break
    await sleep(100)
  }
  if (!row) throw new Error('self host row not rendered after Add machine')
  return {rowText: row.innerText.replace(/\n/g, ' | ')}
})()" > "$EVID_WORK/install-start.json" || { cat "$EVID_WORK"/install-start.json 2>/dev/null || true; fail "adding the machine did not start the install"; }

grep -q 'Installing' "$EVID_WORK/install-start.json" || fail "the install did not start after Add machine"

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
  const row = [...document.querySelectorAll('.mach-item')].find((n) => n.querySelector('.lrow__title > span:first-child')?.textContent.trim() === 'self')
  if (!row) throw new Error('self row not rendered after tab switch')
  const refusal = row.querySelector('.lrow-note__msg')?.textContent ?? ''
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
    const row = [...document.querySelectorAll('.mach-item')].find((n) => n.querySelector('.lrow__title > span:first-child')?.textContent.trim() === 'self')
    if (!row) throw new Error('self row vanished during install')
    const refusal = row.querySelector('.lrow-note__msg')?.textContent ?? ''
    if (refusal !== '') throw new Error('install failed: ' + refusal)
    const install = [...row.querySelectorAll('button')].find((b) => /installing|install prismd|retry/i.test(b.textContent))
    if (!install) return {done: true, rowText: row.innerText.replace(/\n/g, ' | ')}
    await sleep(1000)
  }
  throw new Error('install button still present after 120s')
})()" 150000 > "$EVID_WORK/install-wait.json" || { cat "$EVID_WORK"/install-wait.json 2>/dev/null || true; fail "install did not converge in the UI"; }

grep -q '"done":true' "$EVID_WORK/install-wait.json" || fail "install wait did not record completion"

echo "==> the host got its daemon port and auto sync pushed this machine's config"
curl -sf "http://127.0.0.1:$PORT/api/v1/hosts" > "$EVID_WORK/hosts-after.json" || fail "hosts list after install"
grep -q "\"daemonPort\":$REMOTE_PORT" "$EVID_WORK/hosts-after.json" || fail "host self did not get its own daemon port"
grep -q '"autoSync":true' "$EVID_WORK/hosts-after.json" || fail "auto sync is not on by default for the installed host"

curl -sf "http://127.0.0.1:$REMOTE_PORT/api/v1/health" > "$EVID_WORK/installed-health.json" || fail "the installed daemon is not answering on $REMOTE_PORT"
grep -q '"status":"ok"' "$EVID_WORK/installed-health.json" || fail "installed daemon health is not ok"
ssh -o BatchMode=yes localhost "test -x ~/.prism/remote/prism && tail -c 2000 ~/.prism/remote/prism.log" > "$EVID_WORK/installed-daemon.log" 2>/dev/null || true
ssh -o BatchMode=yes localhost "test -x ~/.prism/remote/prism" || fail "prism binary was not placed in ~/.prism/remote on the ssh target"

cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
  const findSync = () => [...document.querySelectorAll('.mach-item')]
    .find((n) => n.querySelector('.lrow__title > span:first-child')?.textContent.trim() === 'self')
    ?.querySelector('.mach-sync')
  let text = ''
  for (let i = 0; i < 150; i++) {
    text = findSync()?.querySelector('.mach-sync__status')?.textContent.trim() ?? ''
    if (/^Synced|^Sync failed/.test(text)) break
    await sleep(400)
  }
  const sync = findSync()
  if (!sync) throw new Error('Auto sync control not rendered after install')
  if (!sync.querySelector('input[type=checkbox]').checked) throw new Error('Auto sync switch is off after install')
  if (!/^Synced/.test(text)) throw new Error('first sync did not succeed: ' + text)
  return {text}
})()" 70000 > "$EVID_WORK/first-sync.json" || { cat "$EVID_WORK"/first-sync.json 2>/dev/null || true; fail "the first sync did not show as Synced"; }

curl -sf "http://127.0.0.1:$REMOTE_PORT/api/v1/providers" > "$EVID_WORK/installed-providers.json" || fail "installed daemon providers"
grep -q 'gpt-5.6-luna' "$EVID_WORK/installed-providers.json" || fail "installed daemon did not receive this machine's providers"

echo "==> Manage routes through the installed daemon"
cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
  const row = [...document.querySelectorAll('.mach-item')].find((n) => n.querySelector('.lrow__title > span:first-child')?.textContent.trim() === 'self')
  const manage = [...row.querySelectorAll('button')].find((b) => /^manage$/i.test(b.textContent.trim()))
  if (!manage || manage.disabled) throw new Error('Manage button not enabled after install')
  manage.click()
  for (let i = 0; i < 60; i++) {
    if (/managing self/i.test(document.body.innerText)) break
    await sleep(500)
  }
  if (!/managing self/i.test(document.body.innerText)) throw new Error('banner did not switch to Managing self')
  const bannerText = document.querySelector('.banner')?.innerText ?? ''
  if (!bannerText.includes('Settings sync from this machine.')) throw new Error('sync note missing from the banner: ' + bannerText)
  await sleep(3000)
  const nav = (l) => [...document.querySelectorAll('nav button')].find((b) => b.textContent.trim() === l)
  nav('Providers').click()
  await sleep(2500)
  const main = document.querySelector('main')?.innerText ?? ''
  if (/loading/i.test(main)) throw new Error('Providers still loading on the installed daemon')
  if (!main.includes('gpt-5.6-luna')) throw new Error('synced provider model not shown on the installed daemon: ' + main.slice(0, 120))
  return {banner: /managing self/i.test(document.body.innerText), providersSnippet: main.slice(0, 100)}
})()" 60000 > "$EVID_WORK/manage-installed.json" || { cat "$EVID_WORK"/manage-installed.json 2>/dev/null || true; fail "Manage did not route through the installed daemon"; }

grep -q '"banner":true' "$EVID_WORK/manage-installed.json" || fail "manage evidence missing banner switch"

echo "VERIFIED remote daemon install in the real UI"
