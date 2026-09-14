#!/bin/bash
set -euo pipefail

fail() {
  echo "FAIL: $1" >&2
  exit 1
}

REPO_ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd -P)
GO_ROOT="${GO_ROOT:-$REPO_ROOT}"
PORT="${PRISM_PORT:-${PRISMCTL_PROOF_PORT:-18793}}"
CDP_PORT="${PRISM_CDP_PORT:-19224}"
HEADLESS="${PRISM_HEADLESS:-1}"
if curl -sf --max-time 3 -o /dev/null "http://127.0.0.1:$PORT/api/v1/health" 2>/dev/null; then
  fail "a daemon is already bound to port $PORT; stop it before the desktop controls proof"
fi

RUNDIR=$(mktemp -d /tmp/prism-desktopctrl.XXXXXX)
RUN_ID="$(date +%Y%m%d-%H%M%S).$$"
EVID_FINAL="${PRISM_VERIFY_EVIDENCE_DIR:-$REPO_ROOT/verify/evidence/desktop-controls/$RUN_ID}"
EVID_WORK="$RUNDIR/evidence"
mkdir -p "$EVID_WORK" "$RUNDIR/.prism" "$RUNDIR/home" "$RUNDIR/home/.grok" "$RUNDIR/home/.omp/agent" "$RUNDIR/home/.codex"

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
  if [ "$PORT" != "18787" ] && curl -sf --max-time 2 -o /dev/null "http://127.0.0.1:$PORT/api/v1/health" 2>/dev/null; then
    OPID=$(pgrep -f "prismd --listen 127.0.0.1:$PORT" 2>/dev/null | head -1)
    [ -n "$OPID" ] && kill -TERM "$OPID" 2>/dev/null
  fi
  [ -f "$RUNDIR/app.log" ] && cp "$RUNDIR/app.log" "$EVID_WORK/app.log"
  [ -f "$RUNDIR/build.log" ] && cp "$RUNDIR/build.log" "$EVID_WORK/build.log"
  mkdir -p "$EVID_FINAL"
  cp -R "$EVID_WORK"/. "$EVID_FINAL"/
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

goto_view() {
  cdp_eval "await (async () => {
    const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
    for (let i = 0; i < 60; i++) {
      const nav = [...document.querySelectorAll('nav button')].find((node) => node.textContent.includes('$1'))
      if (nav) {
        nav.click()
        for (let j = 0; j < 40; j++) {
          if (document.querySelector('main h1')?.textContent.trim() === '$1') return true
          await sleep(100)
        }
        throw new Error('$1 view did not open after navigation click')
      }
      await sleep(500)
    }
    throw new Error('$1 navigation button not found (app did not render within timeout)')
  })()" > /dev/null
}

wait_for_daemon_ready() {
  cdp_eval "await (async () => {
    const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
    for (let i = 0; i < 120; i++) {
      const text = document.querySelector('main')?.innerText ?? ''
      if (/ready|running/i.test(text)) return {state: 'ready', text: text.slice(0, 120)}
      await sleep(500)
    }
    throw new Error('daemon did not reach ready state: ' + (document.querySelector('main')?.innerText ?? '').slice(0, 200))
  })()" > "$EVID_WORK/daemon-ready.json"
}

echo "==> daemon state and endpoint on the Overview card"
goto_view "Overview"
wait_for_daemon_ready
cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
  for (let i = 0; i < 120; i++) {
    const main = document.querySelector('main')
    const card = main?.querySelector('.ov-card')
    const state = card?.querySelector('.kv-v')
    const endpoint = card?.querySelectorAll('.kv-v')[1]
    if (
      card && state && endpoint &&
      /^ready$|^operational$/i.test(state.textContent.trim()) &&
      endpoint.textContent.trim() === 'http://127.0.0.1:$PORT'
    ) {
      return {state: state.textContent.trim(), endpoint: endpoint.textContent.trim()}
    }
    await sleep(500)
  }
  throw new Error('Overview daemon card did not render ready state and endpoint')
})()" > "$EVID_WORK/overview-daemon.json"

curl -sf "http://127.0.0.1:$PORT/api/v1/health" > "$EVID_WORK/health.json" || fail "daemon health failed"

echo "==> provider create/edit/toggle/delete through the real UI"
goto_view "Providers"
cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
  const add = [...document.querySelectorAll('button')].find((b) => /new provider/i.test(b.textContent))
  if (!add) throw new Error('New provider button not found')
  add.click()
  await sleep(400)
  const idInput = document.querySelector('#prov-id')
  if (!idInput) throw new Error('provider id input not found')
  idInput.focus()
  document.execCommand('insertText', false, 'ui-probe')
  const wireSelect = document.querySelector('#prov-wire')
  if (!wireSelect) throw new Error('provider wire select not found')
  wireSelect.value = 'responses'
  wireSelect.dispatchEvent(new Event('change', {bubbles: true}))
  const urlInput = document.querySelector('#prov-base')
  if (!urlInput) throw new Error('provider base URL input not found')
  urlInput.focus()
  document.execCommand('insertText', false, 'http://127.0.0.1:9/v1')
  const create = [...document.querySelectorAll('button')].find((b) => /create provider/i.test(b.textContent))
  if (!create) throw new Error('Create provider button not found')
  create.click()
  for (let i = 0; i < 40; i++) {
    const card = [...document.querySelectorAll('section.card')].find((node) => node.querySelector('h2, h3')?.textContent.trim() === 'ui-probe')
    if (card) return {created: true, text: card.innerText.slice(0, 200)}
    await sleep(250)
  }
  throw new Error('provider ui-probe did not appear after Create: ' + document.querySelector('main')?.innerText.slice(0, 300))
})()" > "$EVID_WORK/provider-create.json"

cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
  const findCard = () => [...document.querySelectorAll('section.card')].find((node) => node.querySelector('h2, h3')?.textContent.trim() === 'ui-probe')
  if (!findCard()) {
    const row = [...document.querySelectorAll('.prov-prow')].find((b) => b.textContent.includes('ui-probe'))
    if (!row) throw new Error('ui-probe row not found in provider rail')
    row.click()
    await sleep(500)
  }
  const addInput = document.querySelector('.prov-addinput')
  if (!addInput) throw new Error('model add input not found on ui-probe card')
  addInput.focus()
  document.execCommand('insertText', false, 'ui-probe-model')
  const addBtn = [...document.querySelectorAll('button')].find((b) => /^add$/i.test(b.textContent.trim()))
  if (!addBtn) throw new Error('model Add button not found')
  addBtn.click()
  await sleep(700)
  const modelCheckbox = () => findCard()?.querySelector('.model-toggles input[type=checkbox]')
  if (!modelCheckbox()) throw new Error('model toggle checkbox not found on ui-probe card')
  modelCheckbox().click()
  await sleep(700)
  const offText = document.querySelector('.prov-detail')?.innerText ?? ''
  if (!/1 off/i.test(offText)) throw new Error('model toggle did not disable the model: ' + offText.slice(0, 200))
  modelCheckbox().click()
  await sleep(700)
  const restoredText = document.querySelector('.prov-detail')?.innerText ?? ''
  if (!/0 off/i.test(restoredText)) throw new Error('model toggle did not re-enable the model: ' + restoredText.slice(0, 200))
  return {toggled: true, text: restoredText.slice(0, 200)}
})()" > "$EVID_WORK/provider-model-toggle.json"

cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
  const card = [...document.querySelectorAll('section.card')].find((node) => node.querySelector('h2, h3')?.textContent.trim() === 'ui-probe')
  if (!card) throw new Error('ui-probe card not found')
  const del = [...card.querySelectorAll('button')].find((b) => /delete|remove/i.test(b.textContent))
  if (!del) throw new Error('Delete button not found on ui-probe card')
  del.click()
  await sleep(300)
  const current = [...document.querySelectorAll('section.card')].find((node) => node.querySelector('h2, h3')?.textContent.trim() === 'ui-probe')
  const confirm = [...(current?.querySelectorAll('button') ?? []), ...document.querySelectorAll('dialog button, [role=dialog] button')].find((b) => /confirm|delete|remove/i.test(b.textContent) && b !== del)
  if (confirm) confirm.click()
  for (let i = 0; i < 40; i++) {
    const gone = ![...document.querySelectorAll('section.card')].some((node) => node.querySelector('h2, h3')?.textContent.trim() === 'ui-probe')
    if (gone) return {deleted: true}
    await sleep(250)
  }
  throw new Error('ui-probe card still present after delete')
})()" > "$EVID_WORK/provider-delete.json"

echo "==> integrations Rollback through the real UI"
goto_view "Integrations"
cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
  for (let i = 0; i < 40; i++) {
    if (document.querySelectorAll('.int-row-wrap').length >= 3) break
    await sleep(250)
  }
  const card = [...document.querySelectorAll('.int-row-wrap')].find((node) => node.querySelector('.int-name')?.textContent.trim() === 'grok')
  if (!card) throw new Error('grok integration row not found')
  const apply = [...card.querySelectorAll('button')].find((b) => b.textContent.trim() === 'Apply')
  if (!apply) throw new Error('Apply button not found on grok row')
  apply.click()
  for (let i = 0; i < 60; i++) {
    const current = [...document.querySelectorAll('.int-row-wrap')].find((node) => node.querySelector('.int-name')?.textContent.trim() === 'grok')
    if (current?.querySelector('.int-status')?.textContent.trim() === 'managed') break
    await sleep(250)
  }
  const managed = [...document.querySelectorAll('.int-row-wrap')].find((node) => node.querySelector('.int-name')?.textContent.trim() === 'grok')
  if (managed?.querySelector('.int-status')?.textContent.trim() !== 'managed') throw new Error('grok did not become managed after Apply')
  const rollback = [...(managed?.querySelectorAll('button') ?? [])].find((b) => /rollback/i.test(b.textContent))
  if (!rollback) throw new Error('Rollback button not found on managed grok card')
  rollback.click()
  for (let i = 0; i < 60; i++) {
    const current = [...document.querySelectorAll('.int-row-wrap')].find((node) => node.querySelector('.int-name')?.textContent.trim() === 'grok')
    const state = current?.querySelector('.int-status')?.textContent.trim()
    if (state === 'unmanaged') return {rolledBack: true, state}
    await sleep(250)
  }
  throw new Error('grok still managed after Rollback click')
})()" > "$EVID_WORK/integrations-rollback.json"

echo "==> unknown hash falls back to the dashboard"
cdp_eval "await (async () => {
  location.hash = '#/no-such-view'
  await new Promise((resolve) => setTimeout(resolve, 600))
  const h1 = document.querySelector('main h1')?.textContent.trim()
  return {hash: location.hash, h1}
})()" > "$EVID_WORK/unknown-hash.json"
grep -q "Overview" "$EVID_WORK/unknown-hash.json" || fail "unknown hash did not fall back to Overview: $(cat "$EVID_WORK/unknown-hash.json")"

echo "==> quitting Electron and checking daemon teardown"
kill -TERM "$APP_PID"
wait "$APP_PID" 2>/dev/null || true
APP_PID=
sleep 1
if curl -sf "http://127.0.0.1:$PORT/api/v1/health" >/dev/null 2>&1; then
  fail "daemon still reachable after Electron quit"
fi

echo "desktop controls proof OK (daemon state/endpoint on Overview and teardown on quit, provider create/toggle/delete, rollback, unknown hash)"
