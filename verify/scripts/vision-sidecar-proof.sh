#!/bin/bash
set -euo pipefail

fail() {
  echo "FAIL: $1" >&2
  exit 1
}

REPO_ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)
GO_ROOT="${GO_ROOT:-$REPO_ROOT}"
PORT="${PRISM_PORT:-18795}"
CDP_PORT="${PRISM_CDP_PORT:-19225}"
HEADLESS="${PRISM_HEADLESS:-1}"
if curl -sf --max-time 3 -o /dev/null "http://127.0.0.1:$PORT/api/v1/health" 2>/dev/null; then
  fail "a daemon is already bound to port $PORT; stop it before the vision sidecar proof"
fi

RUNDIR=$(mktemp -d /tmp/prism-sidecar.XXXXXX)
RUN_ID="$(date +%Y%m%d-%H%M%S).$$"
EVID_FINAL="${PRISM_VERIFY_EVIDENCE_DIR:-$REPO_ROOT/verify/evidence/vision-sidecar/$RUN_ID}"
EVID_WORK="$RUNDIR/evidence"
mkdir -p "$EVID_WORK" "$RUNDIR/.prism" "$RUNDIR/home"

cleanup() {
  status=$?
  trap - EXIT INT TERM
  set +e
  if [ -n "${APP_PID:-}" ] && kill -0 "$APP_PID" 2>/dev/null; then
    kill -TERM "$APP_PID" 2>/dev/null
    wait "$APP_PID" 2>/dev/null
  fi
  mkdir -p "$EVID_FINAL"
  [ -f "$RUNDIR/app.log" ] && cp "$RUNDIR/app.log" "$EVID_FINAL/app.log"
  cp -R "$EVID_WORK"/. "$EVID_FINAL"/ 2>/dev/null || true
  rm -rf "$RUNDIR"
  echo "evidence: $EVID_FINAL"
  exit "$status"
}
trap cleanup EXIT INT TERM

cat > "$RUNDIR/.prism/prism.json" <<CONFIG
{
  "generation": 0,
  "config": {
    "version": 1,
    "daemon": {"listen": "127.0.0.1:$PORT"},
    "providers": {
      "router": {
        "wire": "chat",
        "baseURL": "http://127.0.0.1:9/v1",
        "models": ["glm-5.3", "gpt-5.6-luna"],
        "modelSettings": {
          "gpt-5.6-luna": {"imageInput": true}
        }
      }
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
echo "==> launching isolated Electron app"
PRISMD_PATH="$RUNDIR/prismd" \
PRISM_PORT="$PORT" \
PRISM_DAEMON_CONFIG="$RUNDIR/.prism/prism.json" \
PRISM_HEADLESS="$HEADLESS" \
HOME="$RUNDIR/home" \
"$REPO_ROOT/node_modules/.bin/electron" "$REPO_ROOT/apps/desktop" \
  --user-data-dir="$RUNDIR/electron" \
  --remote-debugging-port="$CDP_PORT" > "$RUNDIR/app.log" 2>&1 &
APP_PID=$!

for _ in $(seq 1 120); do
  kill -0 "$APP_PID" 2>/dev/null || { cat "$RUNDIR/app.log" 2>/dev/null; fail "electron exited during startup"; }
  curl -sf "http://127.0.0.1:$CDP_PORT/json/version" >/dev/null 2>&1 && break
  sleep 0.25
done
WS=$(node "$REPO_ROOT/verify/scripts/cdp-ws.mjs" "$CDP_PORT") || fail "no Electron CDP page target"

cdp_eval() {
  node "$REPO_ROOT/verify/scripts/cdp-eval.mjs" "$WS" "$1" 90000
}

cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
  for (let i = 0; i < 120; i++) {
    const status = await window.prism.daemon.status()
    if (status.state === 'ready') return {state: 'ready', pid: status.pid}
    await sleep(500)
  }
  throw new Error('daemon did not reach ready state')
})()" > "$EVID_WORK/daemon-ready.json"

cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
  location.hash = '#/stats'
  for (let i = 0; i < 40; i++) {
    if (document.querySelector('main h1')?.textContent.trim() === 'Stats') break
    await sleep(100)
  }
  location.hash = '#/overview'
  for (let i = 0; i < 40; i++) {
    if (document.querySelector('main h1')?.textContent.trim() === 'Overview') return {remounted: true}
    await sleep(100)
  }
  throw new Error('Overview did not remount after daemon became ready')
})()" > /dev/null

echo "==> vision sidecar card is hidden while the experimental flag is off"
cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
  for (let i = 0; i < 20; i++) {
    const card = [...document.querySelectorAll('section.card')].find((node) => node.querySelector('.ov-title')?.textContent.trim() === 'Vision sidecar')
    if (card) throw new Error('vision sidecar card rendered while the flag is off')
    if (document.querySelector('main h1')?.textContent.trim() === 'Overview' && document.querySelector('.ov-grid')) break
    await sleep(250)
  }
  return {hidden: true}
})()" > "$EVID_WORK/sidecar-hidden.json"
grep -q '"hidden":true' "$EVID_WORK/sidecar-hidden.json" || fail "sidecar card not gated by the experimental flag"

echo "==> enabling the experimental vision sidecar flag"
cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
  const nav = (l) => [...document.querySelectorAll('nav button')].find((b) => b.textContent.trim() === l)
  nav('Experimental').click()
  for (let i = 0; i < 60; i++) { if (document.querySelector('main h1')?.textContent.trim() === 'Experimental') break; await sleep(100) }
  if (document.querySelector('main h1')?.textContent.trim() !== 'Experimental') throw new Error('Experimental view did not open')
  const card = [...document.querySelectorAll('.cards .card')].find((node) => node.textContent.includes('Vision sidecar'))
  if (!card) throw new Error('vision sidecar flag card not rendered')
  const toggle = card.querySelector('.toggle input[type=checkbox]')
  if (!toggle) throw new Error('vision sidecar flag toggle not rendered')
  if (!toggle.checked) toggle.click()
  await sleep(200)
  if (!card.querySelector('.toggle input[type=checkbox]').checked) throw new Error('toggle did not switch on')
  nav('Overview').click()
  for (let i = 0; i < 60; i++) { if (document.querySelector('main h1')?.textContent.trim() === 'Overview') return {enabled: true}; await sleep(100) }
  throw new Error('Overview did not reopen after enabling the flag')
})()" > "$EVID_WORK/flag-enable.json"
grep -q '"enabled":true' "$EVID_WORK/flag-enable.json" || fail "vision sidecar flag toggle failed"

echo "==> vision sidecar card renders with disabled state and eligible model"
cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
  const card = () => [...document.querySelectorAll('section.card')].find((node) => node.querySelector('.ov-title')?.textContent.trim() === 'Vision sidecar')
  for (let i = 0; i < 60; i++) {
    const c = card()
    const toggle = c?.querySelector('.toggle input[type=checkbox]')
    const btn = c?.querySelector('.vsd__btn')
    if (c && toggle && btn) {
      const initial = {
        toggleChecked: toggle.checked,
        current: btn.querySelector('.vsd__cur')?.textContent.trim() ?? null,
      }
      btn.click()
      await sleep(300)
      const opts = [...(c.querySelectorAll('.vsd__opt') ?? [])].map((o) => o.querySelector('.vsd__opt-name')?.textContent.trim())
      return {...initial, options: opts}
    }
    await sleep(500)
  }
  throw new Error('vision sidecar card never rendered with toggle and dropdown: ' + (document.querySelector('main')?.innerText ?? '').slice(0, 400))
})()" > "$EVID_WORK/sidecar-initial.json"

grep -q 'router/gpt-5.6-luna' "$EVID_WORK/sidecar-initial.json" || fail "eligible model not listed: $(cat "$EVID_WORK/sidecar-initial.json")"
grep -q 'router/glm-5.3' "$EVID_WORK/sidecar-initial.json" && fail "text-only model wrongly listed as eligible"
grep -q '"toggleChecked":false' "$EVID_WORK/sidecar-initial.json" || fail "sidecar toggle should start disabled"

echo "==> selecting the vision model enables the sidecar through the real UI"
cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
  const card = () => [...document.querySelectorAll('section.card')].find((node) => node.querySelector('.ov-title')?.textContent.trim() === 'Vision sidecar')
  const btn = card()?.querySelector('.vsd__btn')
  if (!btn) throw new Error('model dropdown button not found')
  btn.click()
  await sleep(300)
  const opt = [...(card()?.querySelectorAll('.vsd__opt') ?? [])].find((node) => node.textContent.includes('router/gpt-5.6-luna'))
  if (!opt) throw new Error('router/gpt-5.6-luna option not found in the dropdown')
  opt.click()
  for (let i = 0; i < 40; i++) {
    if (card()?.querySelector('.toggle input[type=checkbox]')?.checked === true) return {selected: true}
    await sleep(250)
  }
  throw new Error('toggle did not flip to enabled after model selection')
})()" > "$EVID_WORK/sidecar-selected.json"

curl -sf "http://127.0.0.1:$PORT/api/v1/providers" > "$EVID_WORK/providers-after-select.json"
grep -q '"visionSidecar":{"enabled":true,"target":"router/gpt-5.6-luna"}' "$EVID_WORK/providers-after-select.json" \
  || fail "daemon config does not show enabled sidecar: $(cat "$EVID_WORK/providers-after-select.json")"

echo "==> toggling off through the real UI"
cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
  const card = () => [...document.querySelectorAll('section.card')].find((node) => node.querySelector('.ov-title')?.textContent.trim() === 'Vision sidecar')
  const toggle = card()?.querySelector('.toggle input[type=checkbox]')
  if (!toggle || !toggle.checked) throw new Error('sidecar toggle not enabled before switching off')
  toggle.click()
  for (let i = 0; i < 40; i++) {
    if (card()?.querySelector('.toggle input[type=checkbox]')?.checked === false) return {off: true}
    await sleep(250)
  }
  throw new Error('toggle did not flip to disabled')
})()" > "$EVID_WORK/sidecar-toggled-off.json"

curl -sf "http://127.0.0.1:$PORT/api/v1/providers" > "$EVID_WORK/providers-after-off.json"
grep -q '"visionSidecar":{"enabled":true' "$EVID_WORK/providers-after-off.json" \
  && fail "daemon config still shows enabled sidecar after toggle off: $(cat "$EVID_WORK/providers-after-off.json")"
grep -q '"target":"router/gpt-5.6-luna"' "$EVID_WORK/providers-after-off.json" \
  || fail "toggle off lost the chosen target: $(cat "$EVID_WORK/providers-after-off.json")"

echo "==> toggling on again through the real UI keeps the chosen target"
cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
  const card = () => [...document.querySelectorAll('section.card')].find((node) => node.querySelector('.ov-title')?.textContent.trim() === 'Vision sidecar')
  const toggle = card()?.querySelector('.toggle input[type=checkbox]')
  if (!toggle || toggle.checked) throw new Error('sidecar toggle not disabled before switching on')
  toggle.click()
  for (let i = 0; i < 40; i++) {
    if (card()?.querySelector('.toggle input[type=checkbox]')?.checked === true) return {on: true}
    await sleep(250)
  }
  throw new Error('toggle did not flip back to enabled')
})()" > "$EVID_WORK/sidecar-toggled-on.json"

curl -sf "http://127.0.0.1:$PORT/api/v1/providers" > "$EVID_WORK/providers-after-on.json"
grep -q '"visionSidecar":{"enabled":true,"target":"router/gpt-5.6-luna"}' "$EVID_WORK/providers-after-on.json" \
  || fail "daemon config lost the sidecar target after re-enable: $(cat "$EVID_WORK/providers-after-on.json")"

echo "vision sidecar proof OK (flag gate hidden/off, flag enable, card render, eligible model filter, select->enable, toggle off/on, config round-trip)"

