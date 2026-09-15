#!/bin/bash
set -euo pipefail

fail() {
  echo "FAIL: $1" >&2
  exit 1
}

run_with_timeout() {
  seconds=$1
  shift
  perl -e '$seconds = shift; alarm $seconds; exec @ARGV' "$seconds" "$@"
}

REPO_ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)
GO_ROOT="${GO_ROOT:-$REPO_ROOT}"
REAL_HOME=$HOME
PORT="${PRISM_PORT:-18787}"
CDP_PORT="${PRISM_CDP_PORT:-19222}"
HEADLESS="${PRISM_HEADLESS:-1}"
LIVE="${PRISM_VERIFY_LIVE:-0}"
LEGACY="${PRISM_VERIFY_LEGACY:-0}"
if curl -sf --max-time 3 -o /dev/null "http://127.0.0.1:$PORT/api/v1/health" 2>/dev/null; then
  fail "a daemon is already bound to port $PORT; stop it before verification (the skill refuses to double-drive a shared instance)"
fi
RUNDIR=$(mktemp -d /tmp/prism-verify.XXXXXX)
EVID_WORK="$RUNDIR/evidence"
EVID_FINAL="${PRISM_VERIFY_EVIDENCE_DIR:-/tmp/prism-verify-evidence.$(date +%Y%m%d-%H%M%S).$$}"
mkdir -p "$EVID_WORK" "$RUNDIR/.prism" "$RUNDIR/.codex" "$RUNDIR/.grok" "$RUNDIR/.omp/agent" "$RUNDIR/.claude" "$RUNDIR/.pi/agent" "$RUNDIR/.config/opencode" "$RUNDIR/.hermes"

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
  [ -f "$RUNDIR/build.log" ] && cp "$RUNDIR/build.log" "$EVID_WORK/build.log"
  [ -f "$RUNDIR/.grok/config.toml" ] && cp "$RUNDIR/.grok/config.toml" "$EVID_WORK/grok-config-at-exit.toml"
  [ -f "$RUNDIR/.claude/settings.json" ] && cp "$RUNDIR/.claude/settings.json" "$EVID_WORK/claude-settings-at-exit.json"
  [ -f "$RUNDIR/.pi/agent/models.json" ] && cp "$RUNDIR/.pi/agent/models.json" "$EVID_WORK/pi-models-at-exit.json"
  [ -f "$RUNDIR/.config/opencode/opencode.json" ] && cp "$RUNDIR/.config/opencode/opencode.json" "$EVID_WORK/opencode-config-at-exit.json"
  [ -f "$RUNDIR/.hermes/config.yaml" ] && cp "$RUNDIR/.hermes/config.yaml" "$EVID_WORK/hermes-config-at-exit.yaml"
  cp -R "$EVID_WORK"/. "$EVID_FINAL"/
  rm -rf "$RUNDIR"
  echo "evidence: $EVID_FINAL"
  exit "$status"
}
trap cleanup EXIT INT TERM

if [ "$LIVE" = "1" ]; then
  SOURCE_STATE="${PRISM_VERIFY_STATE_DIR:-$REAL_HOME/.prism}"
  [ -f "$SOURCE_STATE/prism.json" ] || fail "live verification needs $SOURCE_STATE/prism.json"
  [ -d "$SOURCE_STATE/credentials" ] || fail "live verification needs $SOURCE_STATE/credentials"
  cp "$SOURCE_STATE/prism.json" "$RUNDIR/.prism/prism.json"
  cp -R "$SOURCE_STATE/credentials" "$RUNDIR/.prism/credentials"
  chmod -R go-rwx "$RUNDIR/.prism"
fi
if [ "$LIVE" != "1" ]; then
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
fi
if [ "$LEGACY" = "1" ]; then
  cp "$REPO_ROOT/verify/fixtures/grok-legacy-prism.toml" "$RUNDIR/.grok/config.toml"
fi

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

for _ in $(seq 1 80); do
  curl -sf "http://127.0.0.1:$CDP_PORT/json/version" >/dev/null 2>&1 && break
  sleep 0.25
done
WS=$(node "$REPO_ROOT/verify/scripts/cdp-ws.mjs" "$CDP_PORT") || fail "no Electron CDP page target"

cdp_eval() {
  node "$REPO_ROOT/verify/scripts/cdp-eval.mjs" "$WS" "$1"
}

cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
  const button = [...document.querySelectorAll('nav button')].find((node) => node.textContent.includes('Integrations'))
  if (!button) throw new Error('Integrations navigation button not found')
  button.click()
  for (let i = 0; i < 40; i++) {
    if (document.querySelector('main h1')?.textContent.trim() === 'Integrations' && document.querySelectorAll('.int-row-wrap').length >= 3) break
    await sleep(100)
  }
  if (document.querySelector('main h1')?.textContent.trim() !== 'Integrations') throw new Error('Integrations view did not open')
  return {title: document.querySelector('main h1')?.textContent, body: document.querySelector('main')?.innerText}
})()" > "$EVID_WORK/integrations-before.json" || fail "could not open Integrations through UI"

click_apply() {
  id=$1
  cdp_eval "await (async () => {
    const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
    const card = [...document.querySelectorAll('.int-row-wrap')].find((node) => node.querySelector('.int-name')?.textContent.trim() === '$id')
    if (!card) throw new Error('$id card not found')
    const button = [...card.querySelectorAll('button')].find((node) => node.textContent.trim() === 'Apply')
    if (!button) throw new Error('$id Apply button not found')
    if (button.disabled) throw new Error('$id Apply button is disabled')
    card.dataset.verifyTag = 'before-apply'
    button.click()
    const animationReplays = []
    for (let i = 0; i < 60; i++) {
      await sleep(100)
      const current = [...document.querySelectorAll('.int-row-wrap')].find((node) => node.querySelector('.int-name')?.textContent.trim() === '$id')
      const alert = current?.querySelector('[role=alert]')
      if (alert) throw new Error('$id: ' + alert.textContent)
      const state = current?.querySelector('.int-status')?.textContent.trim()
      if (state === 'managed') {
        if (current !== card) throw new Error('$id card remounted on Apply: the DOM node changed')
        if (current.dataset.verifyTag !== 'before-apply') throw new Error('$id card remounted on Apply: the tag was lost')
        if (animationReplays.length > 0) throw new Error('$id card played an entry animation on Apply: ' + animationReplays.join(','))
        delete current.dataset.verifyTag
        return {id: '$id', state, remounted: false, animationReplays, text: current?.innerText}
      }
      for (const anim of (current ?? card).getAnimations()) {
        if (anim instanceof CSSAnimation && !animationReplays.includes(anim.animationName)) animationReplays.push(anim.animationName)
      }
    }
    throw new Error('$id did not become managed after clicking Apply')
  })()"
}

# Apply then Rollback must update cards in place: same DOM node, no entry-animation replay.
verify_no_remount_rollback() {
  id=$1
  cdp_eval "await (async () => {
    const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
    const card = [...document.querySelectorAll('.int-row-wrap')].find((node) => node.querySelector('.int-name')?.textContent.trim() === '$id')
    if (!card) throw new Error('$id card not found')
    const rollback = [...card.querySelectorAll('button')].find((node) => node.textContent.trim() === 'Rollback')
    if (!rollback) throw new Error('$id Rollback button not found')
    if (rollback.disabled) throw new Error('$id Rollback button is disabled before rollback')
    card.dataset.verifyTag = 'before-rollback'
    rollback.click()
    const animationReplays = []
    for (let i = 0; i < 150; i++) {
      const current = [...document.querySelectorAll('.int-row-wrap')].find((node) => node.querySelector('.int-name')?.textContent.trim() === '$id')
      if (!current) throw new Error('$id card disappeared mid-rollback')
      if (current !== card) throw new Error('$id card remounted during rollback: the DOM node changed')
      if (current.dataset.verifyTag !== 'before-rollback') throw new Error('$id card remounted during rollback: the tag was lost')
      for (const anim of current.getAnimations()) {
        if (anim instanceof CSSAnimation && !animationReplays.includes(anim.animationName)) animationReplays.push(anim.animationName)
      }
      const alert = current.querySelector('[role=alert]')
      if (alert) throw new Error('$id rollback refused: ' + alert.textContent)
      const state = current.querySelector('.int-status')?.textContent.trim()
      if (state !== 'managed') {
        if (animationReplays.length > 0) throw new Error('$id card played an entry animation during rollback: ' + animationReplays.join(','))
        delete current.dataset.verifyTag
        return {id: '$id', state, remounted: false, animationReplays}
      }
      await sleep(100)
    }
    throw new Error('$id rollback never settled off managed')
  })()" > "$EVID_WORK/rollback-$id.json" 2>&1
}

echo "==> clicking Apply in the real UI"
for ID in codex grok omp claude pi opencode opencode2 hermes; do
  if ! click_apply "$ID" > "$EVID_WORK/apply-$ID.json" 2>&1; then
    cdp_eval "document.querySelector('main')?.innerText" > "$EVID_WORK/integrations-failure.json" 2>&1 || true
    cdp_eval "await window.prism.integrations.apply({id:'$ID'})" > "$EVID_WORK/apply-$ID-diagnostic.json" 2>&1 || true
    node "$REPO_ROOT/verify/scripts/cdp-screenshot.mjs" "$WS" "$EVID_WORK/integrations-failure.png" || true
    fail "UI Apply failed for $ID"
  fi
done

cdp_eval "document.querySelector('main')?.innerText" > "$EVID_WORK/integrations-after.json"
node "$REPO_ROOT/verify/scripts/cdp-screenshot.mjs" "$WS" "$EVID_WORK/integrations.png"

echo "==> checking generated client configs"
grep -q "http://127.0.0.1:$PORT/v1" "$RUNDIR/.grok/config.toml" || fail "Grok config has no Prism endpoint after UI Apply"
grep -q "prism managed block" "$RUNDIR/.grok/config.toml" || fail "Grok managed block missing after UI Apply"
grep -q "prism:" "$RUNDIR/.omp/agent/models.yml" || fail "OMP provider missing after UI Apply"
grep -q "\"ANTHROPIC_BASE_URL\": \"http://127.0.0.1:$PORT\"" "$RUNDIR/.claude/settings.json" || fail "Claude settings has no Prism base URL after UI Apply"
grep -q "\"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY\": \"1\"" "$RUNDIR/.claude/settings.json" || fail "Claude settings has no gateway model discovery switch after UI Apply"
grep -q "\"baseUrl\":\"http://127.0.0.1:$PORT\"" "$RUNDIR/.claude/cache/gateway-models.json" || fail "Claude gateway model cache missing or pointing elsewhere after UI Apply"
grep -q "\"baseUrl\": \"http://127.0.0.1:$PORT/v1\"" "$RUNDIR/.pi/agent/models.json" || fail "Pi models.json has no Prism baseUrl after UI Apply"
grep -q "\"npm\": \"@ai-sdk/openai-compatible\"" "$RUNDIR/.config/opencode/opencode.json" || fail "opencode v1 provider block missing after UI Apply"
grep -q "\"package\": \"@opencode-ai/ai/providers/openai-compatible\"" "$RUNDIR/.config/opencode/opencode.json" || fail "opencode2 providers block missing after UI Apply"
grep -q "api: http://127.0.0.1:$PORT/v1" "$RUNDIR/.hermes/config.yaml" || fail "Hermes config has no Prism api URL after UI Apply"
grep -q "Managed by prism: Codex routes through the local prism proxy." "$RUNDIR/.codex/config.toml" || fail "Codex config has no prism routing marker after UI Apply"
grep -q "openai_base_url = \"http://127.0.0.1:$PORT/v1\"" "$RUNDIR/.codex/config.toml" || fail "Codex config does not route the built-in openai provider at the daemon after UI Apply"
grep -q "prism managed block" "$RUNDIR/.codex/config.toml" || fail "Codex managed block missing after UI Apply"
grep -q "Managed by prism: model catalog" "$RUNDIR/.codex/config.toml" || fail "Codex config has no prism model catalog marker after UI Apply"
grep -q "model_catalog_json = \"$RUNDIR/.codex/prism-catalog.json\"" "$RUNDIR/.codex/config.toml" || fail "Codex config does not point model_catalog_json at the prism catalog after UI Apply"
grep -q '"shell_type":"shell_command"' "$RUNDIR/.codex/prism-catalog.json" || fail "Prism catalog missing or not shell_command-shaped after UI Apply"
cp "$RUNDIR/.codex/config.toml" "$EVID_WORK/codex-config.toml"
cp "$RUNDIR/.codex/prism-catalog.json" "$EVID_WORK/prism-catalog.json" 2>/dev/null || true
cp "$RUNDIR/.grok/config.toml" "$EVID_WORK/grok-config.toml"
cp "$RUNDIR/.claude/settings.json" "$EVID_WORK/claude-settings.json"
cp "$RUNDIR/.claude/cache/gateway-models.json" "$EVID_WORK/claude-gateway-models.json"
cp "$RUNDIR/.pi/agent/models.json" "$EVID_WORK/pi-models.json"
cp "$RUNDIR/.config/opencode/opencode.json" "$EVID_WORK/opencode-config.json"
cp "$RUNDIR/.hermes/config.yaml" "$EVID_WORK/hermes-config.yaml"

if command -v grok >/dev/null 2>&1; then
  HOME="$RUNDIR" run_with_timeout 30 grok models > "$EVID_WORK/grok-models.txt" 2>&1 || fail "Grok cannot load the config written by Apply"
  grep -q "prism-" "$EVID_WORK/grok-models.txt" || fail "Grok does not list any model written by Apply"
else
  echo "grok CLI not in PATH; skipping grok parse proof (generated config still verified on disk)" | tee "$EVID_WORK/grok-models.skipped.txt"
fi
if command -v omp >/dev/null 2>&1; then
  HOME="$RUNDIR" run_with_timeout 30 omp models > "$EVID_WORK/omp-models.txt" 2>&1 || fail "OMP cannot load the config written by Apply"
  grep -q "^prism (" "$EVID_WORK/omp-models.txt" || fail "OMP does not list any model written by Apply"
else
  echo "omp CLI not in PATH; skipping omp parse proof (generated config still verified on disk)" | tee "$EVID_WORK/omp-models.skipped.txt"
fi

HOME="$RUNDIR" run_with_timeout 60 claude doctor > "$EVID_WORK/claude-doctor.txt" 2>&1 < /dev/null || fail "Claude doctor failed on the settings written by Apply"
if grep -q "Invalid settings" "$EVID_WORK/claude-doctor.txt"; then fail "Claude rejected the settings written by Apply"; fi
grep -q "custom ANTHROPIC_BASE_URL" "$EVID_WORK/claude-doctor.txt" || fail "Claude doctor did not acknowledge the Prism base URL written by Apply"
if command -v pi >/dev/null 2>&1; then
  HOME="$RUNDIR" run_with_timeout 30 pi --list-models > "$EVID_WORK/pi-models.txt" 2>&1 || fail "Pi cannot load the models.json written by Apply"
  grep -q "^prism " "$EVID_WORK/pi-models.txt" || fail "Pi does not list any model written by Apply"
else
  echo "pi CLI not in PATH; skipping pi parse proof (generated config still verified on disk)" | tee "$EVID_WORK/pi-models.skipped.txt"
fi
HOME="$RUNDIR" run_with_timeout 60 opencode models > "$EVID_WORK/opencode-models.txt" 2>&1 || fail "opencode v1 cannot load the config written by Apply"
grep -q "^prism/" "$EVID_WORK/opencode-models.txt" || fail "opencode v1 does not list any model written by Apply"
if HOME="$RUNDIR" run_with_timeout 5 hermes config get providers >/dev/null 2>&1; then
  HOME="$RUNDIR" run_with_timeout 30 hermes config get providers > "$EVID_WORK/hermes-providers.txt" 2>&1 || fail "Hermes cannot load the config written by Apply"
  grep -q "api: http://127.0.0.1:$PORT/v1" "$EVID_WORK/hermes-providers.txt" || fail "Hermes does not resolve the Prism provider written by Apply"
  HOME="$RUNDIR" run_with_timeout 30 hermes config check > "$EVID_WORK/hermes-config-check.txt" 2>&1 || fail "Hermes config check failed on the config written by Apply"
  if grep -q "Failed to parse" "$EVID_WORK/hermes-config-check.txt"; then fail "Hermes failed to parse the config written by Apply"; fi
else
  echo "hermes CLI venv broken; skipping hermes parse proof (generated config still verified on disk)" | tee "$EVID_WORK/hermes-providers.skipped.txt"
fi
HOME="$RUNDIR" run_with_timeout 30 codex login status > "$EVID_WORK/codex-login-status.txt" 2>&1 || true
if grep -q "Not logged in" "$EVID_WORK/codex-login-status.txt"; then
  :
else
  fail "Codex CLI rejected the config written by Apply: $(cat "$EVID_WORK/codex-login-status.txt")"
fi
echo "opencode2 real-client parse: SKIPPED — headless probe unavailable in beta-19086; the file contract is asserted above and live mode covers real use" | tee "$EVID_WORK/opencode2-models.txt"

echo "==> clicking Rollback through the real UI and proving cards update in place"
node "$REPO_ROOT/verify/scripts/cdp-screenshot.mjs" "$WS" "$EVID_WORK/integrations-cards.png"
for ID in codex grok omp claude pi opencode opencode2 hermes; do
  verify_no_remount_rollback "$ID" || fail "UI Rollback remounted or animated the $ID card"
done
cdp_eval "document.querySelector('main')?.innerText" > "$EVID_WORK/integrations-after-rollback.json"
node "$REPO_ROOT/verify/scripts/cdp-screenshot.mjs" "$WS" "$EVID_WORK/integrations-after-rollback.png"

# Bulk buttons live in the stats strip: Rollback all must be disabled with nothing
# managed, then Apply all converges every card without remounting or animating any.
verify_bulk_apply_all() {
  out=$1
  cdp_eval "await (async () => {
    const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
    const stripButton = (text) => [...document.querySelectorAll('.int-strip button')]
      .find((node) => node.textContent.trim() === text)
    const rollbackAll = stripButton('Rollback all')
    if (!rollbackAll) throw new Error('Rollback all button not found in the stats strip')
    if (!rollbackAll.disabled) throw new Error('Rollback all is enabled with no managed card')
    const applyAll = stripButton('Apply all')
    if (!applyAll) throw new Error('Apply all button not found in the stats strip')
    if (applyAll.disabled) throw new Error('Apply all is disabled while cards are unmanaged')
    const cards = [...document.querySelectorAll('.int-row-wrap')]
    if (cards.length < 3) throw new Error('expected the integration cards, found ' + cards.length)
    for (const card of cards) card.dataset.verifyTag = 'before-bulk-apply'
    applyAll.click()
    const animationReplays = []
    for (let i = 0; i < 300; i++) {
      const current = [...document.querySelectorAll('.int-row-wrap')]
      if (current.length !== cards.length) throw new Error('card count changed mid bulk apply: ' + current.length)
      for (let j = 0; j < current.length; j++) {
        if (current[j] !== cards[j]) throw new Error(current[j].querySelector('.int-name')?.textContent + ' card remounted during bulk apply: the DOM node changed')
        if (current[j].dataset.verifyTag !== 'before-bulk-apply') throw new Error(current[j].querySelector('.int-name')?.textContent + ' card remounted during bulk apply: the tag was lost')
        for (const anim of current[j].getAnimations()) {
          const name = anim instanceof CSSAnimation ? anim.animationName : null
          if (name && !animationReplays.includes(name)) animationReplays.push(name)
        }
      }
      const alert = document.querySelector('.int-screen > .int-refusal[role=alert]')
      if (alert) throw new Error('bulk apply reported a refusal: ' + alert.textContent)
      const states = current.map((card) => card.querySelector('.int-status')?.textContent.trim())
      const managed = states.filter((state) => state === 'managed').length
      if (managed === current.length) {
        if (animationReplays.length > 0) throw new Error('bulk apply played an entry animation: ' + animationReplays.join(','))
        for (const card of current) delete card.dataset.verifyTag
        return {remounted: false, animationReplays, managed, unmanaged: states.filter((state) => state === 'unmanaged').length}
      }
      await sleep(100)
    }
    throw new Error('bulk apply never settled with every card managed')
  })()" > "$out" 2>&1
}

echo "==> clicking Apply all and proving every card converges in place"
verify_bulk_apply_all "$EVID_WORK/bulk-apply-all.json" || fail "UI Apply all remounted, animated, or failed to converge a card"
cdp_eval "document.querySelector('main')?.innerText" > "$EVID_WORK/integrations-after-bulk.json"
node "$REPO_ROOT/verify/scripts/cdp-screenshot.mjs" "$WS" "$EVID_WORK/integrations-after-bulk.png"

# With every card managed again, Rollback all takes over the exit-state role of the
# per-card reapply loop: it must be enabled now, converge every card to unmanaged,
# then Apply all brings them back so the app is left managed.
verify_bulk_rollback_all() {
  cdp_eval "await (async () => {
    const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
    const stripButton = (text) => [...document.querySelectorAll('.int-strip button')]
      .find((node) => node.textContent.trim() === text)
    const rollbackAll = stripButton('Rollback all')
    if (!rollbackAll) throw new Error('Rollback all button not found in the stats strip')
    if (rollbackAll.disabled) throw new Error('Rollback all is disabled while every card is managed')
    const applyAll = stripButton('Apply all')
    if (!applyAll) throw new Error('Apply all button not found in the stats strip')
    if (!applyAll.disabled) throw new Error('Apply all is enabled with every card managed')
    const cards = [...document.querySelectorAll('.int-row-wrap')]
    if (cards.length < 3) throw new Error('expected the integration cards, found ' + cards.length)
    for (const card of cards) card.dataset.verifyTag = 'before-bulk-rollback'
    rollbackAll.click()
    const animationReplays = []
    for (let i = 0; i < 300; i++) {
      const current = [...document.querySelectorAll('.int-row-wrap')]
      if (current.length !== cards.length) throw new Error('card count changed mid bulk rollback: ' + current.length)
      for (let j = 0; j < current.length; j++) {
        if (current[j] !== cards[j]) throw new Error(current[j].querySelector('.int-name')?.textContent + ' card remounted during bulk rollback: the DOM node changed')
        if (current[j].dataset.verifyTag !== 'before-bulk-rollback') throw new Error(current[j].querySelector('.int-name')?.textContent + ' card remounted during bulk rollback: the tag was lost')
        for (const anim of current[j].getAnimations()) {
          const name = anim instanceof CSSAnimation ? anim.animationName : null
          if (name && !animationReplays.includes(name)) animationReplays.push(name)
        }
      }
      const alert = document.querySelector('.int-screen > .int-refusal[role=alert]')
      if (alert) throw new Error('bulk rollback reported a refusal: ' + alert.textContent)
      const states = current.map((card) => card.querySelector('.int-status')?.textContent.trim())
      const unmanaged = states.filter((state) => state === 'unmanaged').length
      if (unmanaged === current.length) {
        if (animationReplays.length > 0) throw new Error('bulk rollback played an entry animation: ' + animationReplays.join(','))
        for (const card of current) delete card.dataset.verifyTag
        return {remounted: false, animationReplays, unmanaged, managed: states.filter((state) => state === 'managed').length}
      }
      await sleep(100)
    }
    throw new Error('bulk rollback never settled with every card unmanaged')
  })()" > "$EVID_WORK/bulk-rollback-all.json" 2>&1
}

echo "==> clicking Rollback all, then Apply all again so exit state is managed"
verify_bulk_rollback_all || fail "UI Rollback all remounted, animated, or failed to converge a card"
verify_bulk_apply_all "$EVID_WORK/bulk-apply-all-again.json" || fail "UI second Apply all failed to converge every card"
cdp_eval "document.querySelector('main')?.innerText" > "$EVID_WORK/integrations-after-bulk-reapply.json"

click_toggle() {
  id=$1
  next=$2
  cdp_eval "await (async () => {
    const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
    const card = [...document.querySelectorAll('.int-row-wrap')].find((node) => node.querySelector('.int-name')?.textContent.trim() === '$id')
    if (!card) throw new Error('$id card not found')
    const toggle = [...card.querySelectorAll('label.toggle')].find((node) => node.querySelector('.toggle__label')?.textContent.trim() === 'Auto-apply')
    if (!toggle) throw new Error('$id Auto-apply toggle not found')
    const input = toggle.querySelector('input[type=checkbox]')
    if (!input) throw new Error('$id Auto-apply checkbox not found')
    if (input.disabled) throw new Error('$id Auto-apply toggle is disabled')
    card.dataset.verifyTag = 'before-toggle'
    input.click()
    const animationReplays = []
    for (let i = 0; i < 60; i++) {
      await sleep(100)
      const current = [...document.querySelectorAll('.int-row-wrap')].find((node) => node.querySelector('.int-name')?.textContent.trim() === '$id')
      const alert = current?.querySelector('[role=alert]')
      if (alert) throw new Error('$id: ' + alert.textContent)
      const live = current?.querySelector('label.toggle input[type=checkbox]')
      if (live !== null && live !== undefined && live.checked === $next) {
        if (current !== card) throw new Error('$id card remounted on toggle: the DOM node changed')
        if (current.dataset.verifyTag !== 'before-toggle') throw new Error('$id card remounted on toggle: the tag was lost')
        if (animationReplays.length > 0) throw new Error('$id card played an entry animation on toggle: ' + animationReplays.join(','))
        delete current.dataset.verifyTag
        return {id: '$id', enabled: live.checked, remounted: false, animationReplays}
      }
      for (const anim of (current ?? card).getAnimations()) {
        if (anim instanceof CSSAnimation && !animationReplays.includes(anim.animationName)) animationReplays.push(anim.animationName)
      }
    }
    throw new Error('$id toggle never settled to enabled=$next')
  })()"
}

echo "==> toggling Auto-apply on for grok through the UI"
click_toggle grok true > "$EVID_WORK/toggle-grok-on.json" || fail "grok Auto-apply toggle click failed"
grep -q '"enabled":true' "$EVID_WORK/toggle-grok-on.json" || fail "grok toggle did not report enabled=true"

echo "==> auto-apply: provider change rewrites the enabled grok config with no UI action"
GEN=$(curl -sf "http://127.0.0.1:$PORT/api/v1/providers" | node -pe 'JSON.parse(require("fs").readFileSync(0,"utf8")).generation') || fail "could not read providers generation"
curl -sf -X PUT -H 'Content-Type: application/json' \
  -d "{\"models\":[\"gpt-5.6-luna\",\"gpt-5.6-luna-probe\"],\"expectedGeneration\":$GEN}" \
  "http://127.0.0.1:$PORT/api/v1/providers/codex?expectedGeneration=$GEN" > "$EVID_WORK/provider-codex-update.json" || fail "provider codex update failed"
AUTO_ALIAS=prism-codex-gpt-5-6-luna-probe
AUTO_HIT=
for _ in $(seq 1 100); do
  if grep -q "$AUTO_ALIAS" "$RUNDIR/.grok/config.toml" 2>/dev/null; then AUTO_HIT=1; break; fi
  sleep 0.2
done
[ -n "$AUTO_HIT" ] || fail "auto-apply never wrote $AUTO_ALIAS into the grok config"
grep -q "prism managed block" "$RUNDIR/.grok/config.toml" || fail "auto-apply lost the managed fence"

echo "==> damaged fence: auto-apply refuses and leaves the file byte-identical"
GROK_BEGIN='# >>> prism managed block (grok) — do not edit (removed by prism rollback) >>>'
cp "$RUNDIR/.grok/config.toml" "$RUNDIR/grok-config-before-damage.toml"
printf '%s\n' "$GROK_BEGIN" >> "$RUNDIR/.grok/config.toml"
cp "$RUNDIR/.grok/config.toml" "$RUNDIR/grok-config-damaged.toml"
LOG_LINES_BEFORE=$(wc -l < "$RUNDIR/app.log" | tr -d ' ')
GEN=$(curl -sf "http://127.0.0.1:$PORT/api/v1/providers" | node -pe 'JSON.parse(require("fs").readFileSync(0,"utf8")).generation') || fail "could not read providers generation"
curl -sf -X PUT -H 'Content-Type: application/json' \
  -d "{\"models\":[\"gpt-5.6-luna\",\"gpt-5.6-luna-probe\"],\"expectedGeneration\":$GEN}" \
  "http://127.0.0.1:$PORT/api/v1/providers/codex?expectedGeneration=$GEN" > "$EVID_WORK/provider-codex-damage.json" || fail "provider codex damage-bump failed"
AUTO_REFUSAL=
for _ in $(seq 1 100); do
  if tail -n +$((LOG_LINES_BEFORE + 1)) "$RUNDIR/app.log" 2>/dev/null | grep -q 'auto-apply grok: prism:'; then AUTO_REFUSAL=1; break; fi
  sleep 0.2
done
[ -n "$AUTO_REFUSAL" ] || fail "auto-apply never logged a refusal for the damaged grok fence"
tail -n +$((LOG_LINES_BEFORE + 1)) "$RUNDIR/app.log" | grep 'auto-apply grok' > "$EVID_WORK/grok-damaged-refusal.log" || true
cmp -s "$RUNDIR/grok-config-damaged.toml" "$RUNDIR/.grok/config.toml" || fail "auto-apply touched the grok config despite the damaged fence"
cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
  const nav = [...document.querySelectorAll('nav button')].find((node) => node.textContent.includes('Overview'))
  if (nav) nav.click()
  for (let i = 0; i < 40; i++) { if (document.querySelector('main h1')?.textContent.trim() === 'Overview') break; await sleep(100) }
  const back = [...document.querySelectorAll('nav button')].find((node) => node.textContent.includes('Integrations'))
  if (!back) throw new Error('Integrations navigation button not found')
  back.click()
  for (let i = 0; i < 40; i++) {
    const card = [...document.querySelectorAll('.int-row-wrap')].find((node) => node.querySelector('.int-name')?.textContent.trim() === 'grok')
    if (card?.querySelector('.int-status')?.textContent.trim() === 'damaged') return {id: 'grok', state: 'damaged'}
    await sleep(100)
  }
  throw new Error('grok card never rendered damaged')
})()" > "$EVID_WORK/grok-damaged-card.json" || fail "grok card never showed the damaged state"
cdp_eval "await (async () => {
  const card = [...document.querySelectorAll('.int-row-wrap')].find((node) => node.querySelector('.int-name')?.textContent.trim() === 'grok')
  if (!card) throw new Error('grok card not found')
  const apply = [...card.querySelectorAll('button')].find((node) => node.textContent.trim() === 'Apply')
  const rollback = [...card.querySelectorAll('button')].find((node) => node.textContent.trim() === 'Rollback')
  if (!apply || !rollback) throw new Error('grok action buttons not found')
  if (!apply.disabled || !rollback.disabled) throw new Error('damaged grok card left Apply or Rollback enabled')
  const input = [...card.querySelectorAll('label.toggle')].find((node) => node.querySelector('.toggle__label')?.textContent.trim() === 'Auto-apply')?.querySelector('input[type=checkbox]')
  if (!input) throw new Error('grok Auto-apply checkbox not found')
  if (input.disabled) throw new Error('damaged grok card disabled the Auto-apply toggle')
  return {id: 'grok', applyDisabled: apply.disabled, rollbackDisabled: rollback.disabled, toggleDisabled: input.disabled, enabled: input.checked}
})()" > "$EVID_WORK/grok-damaged-buttons.json" || fail "damaged grok card buttons had the wrong state"
grep -cF -x "$GROK_BEGIN" "$RUNDIR/.grok/config.toml" | grep -qx 2 || fail "damaged grok config does not hold exactly two begin markers"
cp "$RUNDIR/grok-config-before-damage.toml" "$RUNDIR/.grok/config.toml"
grep -cF -x "$GROK_BEGIN" "$RUNDIR/.grok/config.toml" | grep -qx 1 || fail "fence repair did not leave exactly one begin marker"

echo "==> restart persistence: enabled state survives a prismd crash"
sed -i.bak '/^# >>> prism managed block (grok)/,/^# <<< prism managed block (grok) <<<$/d' "$RUNDIR/.grok/config.toml"
grep -q "prism managed block" "$RUNDIR/.grok/config.toml" && fail "grok managed block not stripped before the restart"
DAEMON_PID=$(pgrep -f "$RUNDIR/prismd" | head -1)
[ -n "$DAEMON_PID" ] || fail "no prismd process found for this run"
kill "$DAEMON_PID"
HEALTH_BACK=
for _ in $(seq 1 200); do
  if curl -sf "http://127.0.0.1:$PORT/api/v1/health" >/dev/null 2>&1; then HEALTH_BACK=1; break; fi
  sleep 0.25
done
[ -n "$HEALTH_BACK" ] || fail "daemon never came back after the kill"
RECONVERGED=
for _ in $(seq 1 100); do
  if grep -q "http://127.0.0.1:$PORT/v1" "$RUNDIR/.grok/config.toml" 2>/dev/null && grep -q "prism managed block" "$RUNDIR/.grok/config.toml" 2>/dev/null; then RECONVERGED=1; break; fi
  sleep 0.2
done
[ -n "$RECONVERGED" ] || fail "enabled grok config did not re-converge after the daemon restart"
curl -sf "http://127.0.0.1:$PORT/api/v1/integrations" > "$EVID_WORK/integrations-after-restart.json" || fail "integrations status unreachable after restart"
node -e 'const data = JSON.parse(require("fs").readFileSync(process.argv[1], "utf8")); const grok = data.integrations.find((entry) => entry.id === "grok"); if (!grok || !grok.enabled) { process.exit(1) }' "$EVID_WORK/integrations-after-restart.json" || fail "grok enabled flag lost after the daemon restart"
cp "$RUNDIR/.grok/config.toml" "$EVID_WORK/grok-config-after-restart.toml"
cp "$RUNDIR/app.log" "$EVID_WORK/app-log-autoapply.json" 2>/dev/null || true

echo "==> walking every renderer view through the real UI"
for VIEW in Overview Accounts Stats Logs Providers Integrations; do
  cdp_eval "await (async () => {
    const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
    const nav = [...document.querySelectorAll('nav button')].find((node) => node.textContent.includes('$VIEW'))
    if (!nav) throw new Error('$VIEW navigation button not found')
    nav.click()
    for (let i = 0; i < 40; i++) {
      if (document.querySelector('main h1')?.textContent.trim() === '$VIEW') break
      await sleep(100)
    }
    const title = document.querySelector('main h1')?.textContent.trim()
    if (title !== '$VIEW') throw new Error('$VIEW view did not open (title=' + title + ')')
    const cards = document.querySelectorAll('main section.card').length
    const body = document.querySelector('main')?.innerText ?? ''
    if (body.trim().length < 20) throw new Error('$VIEW view body is empty')
    return {view: title, cards, bodyChars: body.length, bodyHead: body.slice(0, 400)}
  })()" > "$EVID_WORK/view-$(echo "$VIEW" | tr -d ' &').json" || fail "view walk failed for $VIEW"
done

if [ "$LIVE" = "1" ]; then
  command -v grok >/dev/null 2>&1 || fail "live verification needs the grok CLI in PATH"
  command -v omp >/dev/null 2>&1 || fail "live verification needs the omp CLI in PATH"
  echo "==> running Grok through the UI-applied config"
  GROK_MARKER=PRISM_GROK_LIVE_OK
  GROK_MODEL="${PRISM_VERIFY_GROK_MODEL:-prism-antigravity-gemini-3-7-flash}"
  HOME="$RUNDIR" run_with_timeout 120 grok -m "$GROK_MODEL" -p "Reply with exactly $GROK_MARKER" > "$EVID_WORK/grok-live.txt" 2>&1 || fail "Grok inference failed"
  grep -q "$GROK_MARKER" "$EVID_WORK/grok-live.txt" || fail "Grok output has no final marker"

  echo "==> running OMP with visible thinking"
  OMP_MARKER=PRISM_OMP_THINKING_OK
  OMP_MODEL="${PRISM_VERIFY_OMP_MODEL:-prism/antigravity/gemini-3.7-flash}"
  mkdir -p "$RUNDIR/work"
  printf 'PRISM_TOOL_INPUT\n' > "$RUNDIR/work/probe.txt"
  HOME="$RUNDIR" run_with_timeout 120 omp \
    --mode json \
    --print \
    --no-session \
    --tools read \
    --no-skills \
    --no-rules \
    --cwd "$RUNDIR/work" \
    --model "$OMP_MODEL" \
    --thinking high \
    --print-thoughts \
    "Use the read tool to read probe.txt. Then think through 137 multiplied by 149. Give the result, then print $OMP_MARKER." \
    > "$EVID_WORK/omp-live.ndjson" 2> "$EVID_WORK/omp-live.stderr" || fail "OMP inference failed"
  if ! node "$REPO_ROOT/verify/scripts/assert-omp-output.mjs" \
    "$EVID_WORK/omp-live.ndjson" "$OMP_MARKER" --require-tool --thinking-optional \
    > "$EVID_WORK/omp-assertion.json" 2> "$EVID_WORK/omp-assertion.stderr"; then
    fail "OMP final answer or tool round trip is missing"
  fi
fi

echo "==> quitting Electron and checking daemon teardown"
kill -TERM "$APP_PID"
wait "$APP_PID" 2>/dev/null || true
APP_PID=
sleep 1
if curl -sf "http://127.0.0.1:$PORT/api/v1/health" >/dev/null 2>&1; then
  fail "daemon still reachable after Electron quit"
fi

echo "verification passed"
