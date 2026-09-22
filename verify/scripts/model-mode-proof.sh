#!/bin/bash
set -euo pipefail

fail() { echo "FAIL: $1" >&2; exit 1; }

REPO_ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)
GO_ROOT="${GO_ROOT:-$REPO_ROOT}"
PORT="${PRISM_MODE_PROOF_PORT:-18795}"
CDP_PORT="${PRISM_MODE_CDP_PORT:-19226}"
HEADLESS="${PRISM_HEADLESS:-1}"
if curl -sf --max-time 3 -o /dev/null "http://127.0.0.1:$PORT/api/v1/health" 2>/dev/null; then
  fail "a daemon is already bound to port $PORT"
fi

RUNDIR=$(mktemp -d /tmp/prism-modelmode.XXXXXX)
RUN_ID="$(date +%Y%m%d-%H%M%S).$$"
EVID_FINAL="${PRISM_VERIFY_EVIDENCE_DIR:-$REPO_ROOT/verify/evidence/model-mode/$RUN_ID}"
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
  [ -f "$RUNDIR/app.log" ] && cp "$RUNDIR/app.log" "$EVID_WORK/app.log"
  mkdir -p "$EVID_FINAL"
  cp -R "$EVID_WORK"/. "$EVID_FINAL"/
  rm -rf "$RUNDIR"
  echo "evidence: $EVID_FINAL"
  exit "$status"
}
trap cleanup EXIT INT TERM

# One antigravity family (template gemini-3.7-flash) plus a codex provider
# the switch must refuse. Discovery is faked at the management boundary: the
# sync endpoint is driven against a stub upstream is not needed because the
# mode switch is pure config math over the reviewed table.
cat > "$RUNDIR/.prism/prism.json" <<CONFIG
{
  "generation": 0,
  "config": {
    "version": 1,
    "daemon": {"listen": "127.0.0.1:$PORT"},
    "providers": {
      "antigravity": {
        "wire": "antigravity",
        "models": ["gemini-3.7-flash", "gemini-2.5-pro", "chat_20706"],
        "disabledModels": ["gemini-2.5-pro"],
        "syncedModels": ["gemini-3.7-flash", "gemini-2.5-pro", "chat_20706"],
        "modelSettings": {"gemini-3.7-flash": {"contextWindow": 12345, "reasoningEfforts": ["low", "high"]}}
      },
      "codex": {"wire": "codex", "models": ["gpt-5.6-luna"]}
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
  kill -0 "$APP_PID" 2>/dev/null || fail "electron exited during startup"
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
    throw new Error('$1 navigation button not found')
  })()" > /dev/null
}

echo "==> initial logical view: rungs only on the manually overridden family, no manual badge"
goto_view "Providers"
cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
  for (let i = 0; i < 80; i++) {
    const detail = document.querySelector('.prov-detail')
    if (detail && detail.textContent.includes('gemini-3.7-flash')) return true
    await sleep(250)
  }
  throw new Error('antigravity detail did not render: ' + (document.querySelector('main')?.innerText ?? '').slice(0, 300))
})()" > /dev/null
cdp_eval "await (async () => {
  const detail = document.querySelector('.prov-detail')
  const rows = [...detail.querySelectorAll('.prov-mline')]
  const logicalIds = rows.map((r) => r.querySelector('.pm-name')?.textContent ?? '')
  const badges = rows.map((r) => [...r.querySelectorAll('.prov-mline-manual')].map((b) => b.textContent).join(','))
  const rungRows = rows.filter((r) => r.querySelector('.pm-rungs')).map((r) => r.querySelector('.pm-name')?.textContent ?? '')
  const errors = []
  const wantLogical = ['gemini-2.5-pro', 'gemini-3.7-flash', 'chat_20706'].sort()
  const gotLogical = logicalIds.slice().sort()
  if (JSON.stringify(gotLogical) !== JSON.stringify(wantLogical)) errors.push('logical rows = ' + JSON.stringify(gotLogical))
  if (badges.some((b) => b.includes('manual'))) errors.push('manual badge on synced-style logical rows: ' + JSON.stringify(badges))
  if (JSON.stringify(rungRows) !== JSON.stringify(['gemini-3.7-flash'])) {
    errors.push('effort rungs must render only on the manually overridden model: ' + JSON.stringify(rungRows))
  }
  const rungTexts = [...(rows.find((r) => (r.querySelector('.pm-name')?.textContent ?? '') === 'gemini-3.7-flash')?.querySelectorAll('.pm-rung') ?? [])].map((r) => r.textContent.trim())
  if (JSON.stringify(rungTexts) !== JSON.stringify(['low', 'high'])) {
    errors.push('rung chips = ' + JSON.stringify(rungTexts) + ', want ["low","high"] from the manual override')
  }
  if (errors.length) throw new Error(errors.join('; '))
  return {rows: gotLogical, rungRows}
})()" > "$EVID_WORK/logical-view.json"

echo "==> switch to raw through the UI button"
cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
  const btn = document.querySelector('.pm-rawbtn')
  if (!btn) throw new Error('mode switch button not found')
  if (!/raw models/i.test(btn.textContent)) throw new Error('unexpected button label: ' + btn.textContent)
  btn.click()
  for (let i = 0; i < 80; i++) {
    const detail = document.querySelector('.prov-detail')
    if (detail && detail.textContent.includes('gemini-3.7-flash-low')) return {switched: true}
    await sleep(250)
  }
  throw new Error('raw rows did not appear after switch: ' + (document.querySelector('.prov-detail')?.innerText ?? '').slice(0, 300))
})()" > "$EVID_WORK/raw-switch.json"

cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
  for (let i = 0; i < 80; i++) {
    const detail = document.querySelector('.prov-detail')
    if (detail && detail.textContent.includes('gemini-3.7-flash-high')) break
    await sleep(250)
  }
  const rows = [...document.querySelectorAll('.prov-detail .prov-mline')]
  const ids = rows.map((r) => r.querySelector('.pm-name')?.textContent ?? '')
  const toggles = rows.filter((r) => r.querySelector('.model-toggles input[type=checkbox]')).length
  const settings = rows.filter((r) => [...r.querySelectorAll('.ibtn')].some((b) => (b.getAttribute('aria-label') ?? '').startsWith('Edit '))).length
  const errors = []
  if (!ids.includes('gemini-3.7-flash-low') || !ids.includes('gemini-3.7-flash-high') || !ids.includes('gemini-3.7-flash-medium') || !ids.includes('gemini-3.7-flash-tiered')) {
    errors.push('family members missing from raw rows: ' + JSON.stringify(ids))
  }
  if (!ids.includes('chat_20706')) errors.push('non-family id missing from raw rows: ' + JSON.stringify(ids))
  if (toggles !== rows.length) errors.push('raw rows missing per-model toggles: ' + toggles + ' of ' + rows.length)
  if (settings !== rows.length) errors.push('raw rows missing edit buttons: ' + settings + ' of ' + rows.length)
  if (rows.some((r) => r.classList.contains('prov-mline--off') === false && false)) errors.push('unreachable')
  const offRows = rows.filter((r) => r.classList.contains('prov-mline--off')).map((r) => r.querySelector('.pm-name')?.textContent)
  if (!offRows.includes('gemini-2.5-pro')) errors.push('disabled non-family state lost in raw mode: ' + JSON.stringify(offRows))
  if (errors.length) throw new Error(errors.join('; '))
  return {rows: ids.length, off: offRows}
})()" > "$EVID_WORK/raw-view.json"

echo "==> config and catalog followed the switch"
curl -sf "http://127.0.0.1:$PORT/api/v1/providers" > "$EVID_WORK/providers-raw.json"
node -e "
const fs = require('fs')
const body = JSON.parse(fs.readFileSync('$EVID_WORK/providers-raw.json', 'utf8'))
const ag = body.providers.find((p) => p.id === 'antigravity')
const errs = []
if (ag.modelMode !== 'raw') errs.push('modelMode = ' + ag.modelMode)
for (const m of ['gemini-3.7-flash-low', 'gemini-3.7-flash-medium', 'gemini-3.7-flash-high', 'gemini-3.7-flash-tiered']) {
  if (!ag.models.includes(m)) errs.push('member missing from stored models: ' + m)
  if (!ag.rawModels.includes(m)) errs.push('member missing from rawModels: ' + m)
  if (!ag.disabledModels.includes('gemini-2.5-pro')) {}
}
if (!ag.disabledModels.includes('gemini-2.5-pro')) errs.push('disabled state lost: ' + JSON.stringify(ag.disabledModels))
if (!ag.syncedModels || !ag.syncedModels.includes('gemini-3.7-flash-low')) errs.push('synced list not in raw vocabulary: ' + JSON.stringify(ag.syncedModels))
if (ag.modelSettings['gemini-3.7-flash-low']?.contextWindow !== 12345) errs.push('settings fan-out lost: ' + JSON.stringify(ag.modelSettings))
if (errs.length) { console.error('FAIL: ' + errs.join('; ')); process.exit(1) }
console.log('stored raw state ok: ' + ag.models.length + ' models, mode ' + ag.modelMode)
"
curl -sf "http://127.0.0.1:$PORT/api/v1/models" > "$EVID_WORK/models-raw.json"
node -e "
const fs = require('fs')
const body = JSON.parse(fs.readFileSync('$EVID_WORK/models-raw.json', 'utf8'))
const ids = body.models.map((m) => m.id)
const errs = []
if (!ids.includes('antigravity/gemini-3.7-flash-medium')) errs.push('catalog missing raw member: ' + JSON.stringify(ids.filter((i) => i.startsWith('antigravity'))))
if (ids.includes('antigravity/gemini-3.7-flash')) errs.push('catalog still carries the logical family id in raw mode')
if (errs.length) { console.error('FAIL: ' + errs.join('; ')); process.exit(1) }
console.log('catalog raw state ok: ' + ids.filter((i) => i.startsWith('antigravity')).join(', '))
"

echo "==> switch back to logical through the UI"
cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
  for (let i = 0; i < 80; i++) {
    const btn = document.querySelector('.pm-rawbtn')
    if (btn && /logical models/i.test(btn.textContent)) {
      btn.click()
      await sleep(700)
      const detail = document.querySelector('.prov-detail')
      const names = [...detail.querySelectorAll('.pm-name')].map((n) => n.textContent)
      const want = ['gemini-2.5-pro', 'gemini-3.7-flash', 'chat_20706']
      if (JSON.stringify(names.slice().sort()) === JSON.stringify(want.sort())) return {restored: true, rows: names}
    }
    await sleep(250)
  }
  throw new Error('logical rows did not return after switching back: ' + (document.querySelector('.prov-detail')?.innerText ?? '').slice(0, 400))
})()" > "$EVID_WORK/logical-back.json"

curl -sf "http://127.0.0.1:$PORT/api/v1/providers" > "$EVID_WORK/providers-logical.json"
node -e "
const fs = require('fs')
const body = JSON.parse(fs.readFileSync('$EVID_WORK/providers-logical.json', 'utf8'))
const ag = body.providers.find((p) => p.id === 'antigravity')
const errs = []
if (ag.modelMode !== 'logical' && ag.modelMode !== undefined) errs.push('modelMode = ' + ag.modelMode)
if (!ag.models.includes('gemini-3.7-flash') || ag.models.includes('gemini-3.7-flash-low')) errs.push('logical models = ' + JSON.stringify(ag.models))
if (!ag.disabledModels.includes('gemini-2.5-pro')) errs.push('disabled state lost: ' + JSON.stringify(ag.disabledModels))
if (!ag.syncedModels || !ag.syncedModels.includes('gemini-3.7-flash') || ag.syncedModels.includes('gemini-3.7-flash-low')) errs.push('synced list not in logical vocabulary: ' + JSON.stringify(ag.syncedModels))
if (errs.length) { console.error('FAIL: ' + errs.join('; ')); process.exit(1) }
console.log('round trip ok: ' + JSON.stringify(ag.models))
"

echo "==> codex refuses the switch"
GEN=$(node -e "console.log(JSON.parse(require('fs').readFileSync('$EVID_WORK/providers-logical.json','utf8')).generation)")
CODE=$(curl -s -o "$RUNDIR/codex-mode.json" -w '%{http_code}' -X PUT \
  "http://127.0.0.1:$PORT/api/v1/providers/codex/model-mode?expectedGeneration=$GEN" \
  -H 'Content-Type: application/json' -d '{"mode":"raw","expectedGeneration":'$GEN'}')
if [ "$CODE" != "400" ]; then
  fail "codex model-mode switch returned $CODE, want 400; body=$(cat "$RUNDIR/codex-mode.json")"
fi
cp "$RUNDIR/codex-mode.json" "$EVID_WORK/codex-mode-reject.json"

echo "PASS: model mode switch round trip verified"
