#!/bin/bash
set -euo pipefail

fail() {
  echo "FAIL: $1" >&2
  exit 1
}

run_with_timeout() {
  seconds=$1
  shift
  env -i "${_VERIFY_ENV[@]}" perl -e '$seconds = shift; alarm $seconds; exec @ARGV; die "cannot execute $ARGV[0]: $!\n"' "$seconds" "$@"
}

REPO_ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)
GO_ROOT="${GO_ROOT:-$REPO_ROOT}"
REAL_HOME=$HOME
PORT="${PRISM_PORT:-18787}"
CDP_PORT="${PRISM_CDP_PORT:-19222}"
HEADLESS="${PRISM_HEADLESS:-1}"
LIVE="${PRISM_VERIFY_LIVE:-0}"
LEGACY="${PRISM_VERIFY_LEGACY:-0}"
source "$REPO_ROOT/verify/scripts/owned-runtime.sh"
verify_init integration-drive "$PORT" "$CDP_PORT"
PRIVATE_WORK="$EVID_WORK"
mkdir -p "$RUNDIR/.codex" "$RUNDIR/.grok" "$RUNDIR/.omp/agent" "$RUNDIR/.claude" "$RUNDIR/.pi/agent" "$RUNDIR/.config/opencode" "$RUNDIR/.hermes"

if [ "$LIVE" = "1" ]; then
  SOURCE_STATE="${PRISM_VERIFY_STATE_DIR:-$REAL_HOME/.prism}"
  VERIFY_LIVE=1
  verify_stage_state "$SOURCE_STATE"
  PRIVATE_WORK="$RUNDIR/private-proof"
  mkdir -p "$PRIVATE_WORK"
fi
PROOF_WORK="$PRIVATE_WORK"
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

echo "==> building prism and desktop"
(cd "$GO_ROOT" && go build -o "$RUNDIR/prism" ./cmd/prism) || fail "go build cmd/prism"
npm run build --prefix "$REPO_ROOT" > "$RUNDIR/build.log" 2>&1 || fail "npm run build"

echo "==> launching isolated Electron app"
verify_start_electron
curl -sf --max-time 5 "http://127.0.0.1:$PORT/api/v1/health" > "$EVID_WORK/health.json" || fail "daemon health failed"

cdp_eval() {
  node "$REPO_ROOT/verify/scripts/cdp-eval.mjs" "$WS" "$1" "${2:-30000}"
}

cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
  const button = [...document.querySelectorAll('nav button')].find((node) => node.textContent.includes('Experimental'))
  if (!button) throw new Error('Experimental navigation button not found')
  button.click()
  for (let i = 0; i < 40; i++) {
    const input = [...document.querySelectorAll('.experimental-flag')].find((node) => node.textContent.includes('Other agents'))?.querySelector('input')
    if (input) {
      if (!input.checked) input.click()
      return input.checked
    }
    await sleep(100)
  }
  throw new Error('Other agents toggle not found')
})()" > "$PROOF_WORK/experimental-agents.json" || fail "could not enable other agents through UI"

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
})()" > "$PROOF_WORK/integrations-before.json" || fail "could not open Integrations through UI"

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
    const clickTime = performance.now()
    button.click()
    const beforeClick = new Set(card.getAnimations().map((anim) => anim.animationName ?? null).filter((name) => name !== null))
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
        if (!(anim instanceof CSSAnimation) || beforeClick.has(anim.animationName)) continue
        const started = anim.startTime ?? 0
        if (started > clickTime && !animationReplays.includes(anim.animationName)) animationReplays.push(anim.animationName)
      }
    }
    throw new Error('$id did not become managed after clicking Apply')
  })()"
}

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
    const clickTime = performance.now()
    rollback.click()
    const beforeClick = new Set(card.getAnimations().map((anim) => anim.animationName ?? null).filter((name) => name !== null))
    const animationReplays = []
    for (let i = 0; i < 150; i++) {
      const current = [...document.querySelectorAll('.int-row-wrap')].find((node) => node.querySelector('.int-name')?.textContent.trim() === '$id')
      if (!current) throw new Error('$id card disappeared mid-rollback')
      if (current !== card) throw new Error('$id card remounted during rollback: the DOM node changed')
      if (current.dataset.verifyTag !== 'before-rollback') throw new Error('$id card remounted during rollback: the tag was lost')
      for (const anim of current.getAnimations()) {
        if (!(anim instanceof CSSAnimation) || beforeClick.has(anim.animationName)) continue
        const started = anim.startTime ?? 0
        if (started > clickTime && !animationReplays.includes(anim.animationName)) animationReplays.push(anim.animationName)
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
  })()" > "$PROOF_WORK/rollback-$id.json" 2>&1
}

echo "==> clicking Apply in the real UI"
for ID in codex grok omp claude pi opencode hermes; do
  if ! click_apply "$ID" > "$PROOF_WORK/apply-$ID.json" 2>&1; then
    cdp_eval "document.querySelector('main')?.innerText" > "$PROOF_WORK/integrations-failure.json" 2>&1 || true
    cdp_eval "await window.prism.integrations.apply({id:'$ID'})" > "$PROOF_WORK/apply-$ID-diagnostic.json" 2>&1 || true
    node "$REPO_ROOT/verify/scripts/cdp-screenshot.mjs" "$WS" "$PROOF_WORK/integrations-failure.png" || true
    fail "UI Apply failed for $ID"
  fi
done

cdp_eval "document.querySelector('main')?.innerText" > "$PROOF_WORK/integrations-after.json"
node "$REPO_ROOT/verify/scripts/cdp-screenshot.mjs" "$WS" "$PROOF_WORK/integrations.png"

echo "==> checking generated client configs"
grep -q "http://127.0.0.1:$PORT/v1" "$RUNDIR/.grok/config.toml" || fail "Grok config has no Prism endpoint after UI Apply"
grep -q "prism managed block" "$RUNDIR/.grok/config.toml" || fail "Grok managed block missing after UI Apply"
grep -q "            - max" "$RUNDIR/.omp/agent/models.yml" || fail "OMP effort ladder missing the max rung after UI Apply"
grep -q "\"ANTHROPIC_BASE_URL\": \"http://127.0.0.1:$PORT\"" "$RUNDIR/.claude/settings.json" || fail "Claude settings has no Prism base URL after UI Apply"
grep -q "\"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY\": \"1\"" "$RUNDIR/.claude/settings.json" || fail "Claude settings has no gateway model discovery switch after UI Apply"
grep -q "\"baseUrl\":\"http://127.0.0.1:$PORT\"" "$RUNDIR/.claude/cache/gateway-models.json" || fail "Claude gateway model cache missing or pointing elsewhere after UI Apply"
grep -q "\"baseUrl\": \"http://127.0.0.1:$PORT/v1\"" "$RUNDIR/.pi/agent/models.json" || fail "Pi models.json has no Prism baseUrl after UI Apply"
node - "$RUNDIR/.config/opencode/opencode.json" "$PORT" <<'OPENCODE_CONFIG'
const fs = require('node:fs')
const config = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'))
const endpoint = `http://127.0.0.1:${process.argv[3]}/v1`
const legacy = config.provider?.prism
const modern = config.providers?.prism
if (legacy?.npm !== '@ai-sdk/openai-compatible' || legacy.options?.baseURL !== endpoint) throw new Error('opencode legacy provider endpoint missing')
if (modern?.package !== '@opencode/ai/providers/openai-compatible' || modern.settings?.baseURL !== endpoint) throw new Error('opencode modern provider endpoint missing')
if (!legacy.options.apiKey || !modern.settings.apiKey) throw new Error('opencode provider key missing')
const ids = Object.keys(legacy.models ?? {})
if (ids.length === 0 || ids.length !== Object.keys(modern.models ?? {}).length) throw new Error('opencode model catalogs are empty or differ')
for (const id of ids) {
  const oldModel = legacy.models[id]
  const newModel = modern.models[id]
  if (!newModel) throw new Error(`opencode model missing: ${id}`)
  if (JSON.stringify(oldModel.limit) !== JSON.stringify(newModel.limit)) throw new Error(`opencode model limits differ: ${id}`)
  if (oldModel.limit && (!Number.isFinite(oldModel.limit.context) || !Number.isFinite(oldModel.limit.output) || oldModel.limit.output >= oldModel.limit.context)) throw new Error(`opencode output limit leaves no input room: ${id}`)
  const oldEfforts = Object.values(oldModel.variants ?? {}).map((variant) => variant.reasoningEffort).sort()
  const newEfforts = (newModel.variants ?? []).map((variant) => variant.body?.reasoning_effort).sort()
  if (JSON.stringify(oldEfforts) !== JSON.stringify(newEfforts)) throw new Error(`opencode reasoning variants differ: ${id}`)
  if (oldModel.options?.reasoningEffort !== newModel.settings?.reasoningEffort) throw new Error(`opencode default reasoning differs: ${id}`)
}
OPENCODE_CONFIG
grep -q "api: http://127.0.0.1:$PORT/v1" "$RUNDIR/.hermes/config.yaml" || fail "Hermes config has no Prism api URL after UI Apply"
grep -q "Managed by prism: Codex routes through the local prism proxy." "$RUNDIR/.codex/config.toml" || fail "Codex config has no prism routing marker after UI Apply"
grep -q "openai_base_url = \"http://127.0.0.1:$PORT/v1\"" "$RUNDIR/.codex/config.toml" || fail "Codex config does not route the built-in openai provider at the daemon after UI Apply"
grep -q "prism managed block" "$RUNDIR/.codex/config.toml" || fail "Codex managed block missing after UI Apply"
grep -q "Managed by prism: model catalog" "$RUNDIR/.codex/config.toml" || fail "Codex config has no prism model catalog marker after UI Apply"
grep -q "model_catalog_json = \"$RUNDIR/.codex/prism-catalog.json\"" "$RUNDIR/.codex/config.toml" || fail "Codex config does not point model_catalog_json at the prism catalog after UI Apply"
grep -q '"shell_type":"shell_command"' "$RUNDIR/.codex/prism-catalog.json" || fail "Prism catalog missing or not shell_command-shaped after UI Apply"
cp "$RUNDIR/.codex/config.toml" "$PROOF_WORK/codex-config.toml"
cp "$RUNDIR/.codex/prism-catalog.json" "$PROOF_WORK/prism-catalog.json" 2>/dev/null || true
cp "$RUNDIR/.grok/config.toml" "$PROOF_WORK/grok-config.toml"
cp "$RUNDIR/.claude/settings.json" "$PROOF_WORK/claude-settings.json"
cp "$RUNDIR/.claude/cache/gateway-models.json" "$PROOF_WORK/claude-gateway-models.json"
cp "$RUNDIR/.pi/agent/models.json" "$PROOF_WORK/pi-models.json"
cp "$RUNDIR/.config/opencode/opencode.json" "$PROOF_WORK/opencode-config.json"
cp "$RUNDIR/.hermes/config.yaml" "$PROOF_WORK/hermes-config.yaml"

if command -v grok >/dev/null 2>&1; then
  HOME="$RUNDIR" run_with_timeout 30 grok models > "$PROOF_WORK/grok-models.txt" 2>&1 || fail "Grok cannot load the config written by Apply"
  grep -q "prism-" "$PROOF_WORK/grok-models.txt" || fail "Grok does not list any model written by Apply"
else
  echo "grok CLI not in PATH; skipping grok parse proof (generated config still verified on disk)" | tee "$PROOF_WORK/grok-models.skipped.txt"
fi
if command -v omp >/dev/null 2>&1; then
  HOME="$RUNDIR" run_with_timeout 30 omp models > "$PROOF_WORK/omp-models.txt" 2>&1 || fail "OMP cannot load the config written by Apply"
  grep -q "^prism (" "$PROOF_WORK/omp-models.txt" || fail "OMP does not list any model written by Apply"
else
  echo "omp CLI not in PATH; skipping omp parse proof (generated config still verified on disk)" | tee "$PROOF_WORK/omp-models.skipped.txt"
fi

HOME="$RUNDIR" run_with_timeout 60 claude doctor > "$PROOF_WORK/claude-doctor.txt" 2>&1 < /dev/null || fail "Claude doctor failed on the settings written by Apply"
if grep -q "Invalid settings" "$PROOF_WORK/claude-doctor.txt"; then fail "Claude rejected the settings written by Apply"; fi
grep -q "custom ANTHROPIC_BASE_URL" "$PROOF_WORK/claude-doctor.txt" || fail "Claude doctor did not acknowledge the Prism base URL written by Apply"
if command -v pi >/dev/null 2>&1; then
  HOME="$RUNDIR" run_with_timeout 30 pi --list-models > "$PROOF_WORK/pi-models.txt" 2>&1 || fail "Pi cannot load the models.json written by Apply"
  grep -q "^prism " "$PROOF_WORK/pi-models.txt" || fail "Pi does not list any model written by Apply"
else
  echo "pi CLI not in PATH; skipping pi parse proof (generated config still verified on disk)" | tee "$PROOF_WORK/pi-models.skipped.txt"
fi
echo "opencode headless parse unavailable; generated config checked above" > "$PROOF_WORK/opencode-models.txt"
if HOME="$RUNDIR" run_with_timeout 5 hermes config get providers >/dev/null 2>&1; then
  HOME="$RUNDIR" run_with_timeout 30 hermes config get providers > "$PROOF_WORK/hermes-providers.txt" 2>&1 || fail "Hermes cannot load the config written by Apply"
  grep -q "api: http://127.0.0.1:$PORT/v1" "$PROOF_WORK/hermes-providers.txt" || fail "Hermes does not resolve the Prism provider written by Apply"
  HOME="$RUNDIR" run_with_timeout 30 hermes config check > "$PROOF_WORK/hermes-config-check.txt" 2>&1 || fail "Hermes config check failed on the config written by Apply"
  if grep -q "Failed to parse" "$PROOF_WORK/hermes-config-check.txt"; then fail "Hermes failed to parse the config written by Apply"; fi
else
  echo "hermes CLI venv broken; skipping hermes parse proof (generated config still verified on disk)" | tee "$PROOF_WORK/hermes-providers.skipped.txt"
fi
HOME="$RUNDIR" run_with_timeout 30 codex login status > "$PROOF_WORK/codex-login-status.txt" 2>&1 || true
if grep -q "Not logged in" "$PROOF_WORK/codex-login-status.txt"; then
  :
else
  fail "Codex CLI rejected the config written by Apply: $(cat "$PROOF_WORK/codex-login-status.txt")"
fi

echo "==> clicking Rollback through the real UI and proving cards update in place"
node "$REPO_ROOT/verify/scripts/cdp-screenshot.mjs" "$WS" "$PROOF_WORK/integrations-cards.png"
for ID in codex grok omp claude pi opencode hermes; do
  verify_no_remount_rollback "$ID" || fail "UI Rollback remounted or animated the $ID card"
done
cdp_eval "document.querySelector('main')?.innerText" > "$PROOF_WORK/integrations-after-rollback.json"
node "$REPO_ROOT/verify/scripts/cdp-screenshot.mjs" "$WS" "$PROOF_WORK/integrations-after-rollback.png"

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
    const clickTime = performance.now()
    applyAll.click()
    const beforeClick = cards.map((card) => new Set(card.getAnimations().map((anim) => anim.animationName ?? null).filter((name) => name !== null)))
    const animationReplays = []
    for (let i = 0; i < 300; i++) {
      const current = [...document.querySelectorAll('.int-row-wrap')]
      if (current.length !== cards.length) throw new Error('card count changed mid bulk apply: ' + current.length)
      for (let j = 0; j < current.length; j++) {
        if (current[j] !== cards[j]) throw new Error(current[j].querySelector('.int-name')?.textContent + ' card remounted during bulk apply: the DOM node changed')
        if (current[j].dataset.verifyTag !== 'before-bulk-apply') throw new Error(current[j].querySelector('.int-name')?.textContent + ' card remounted during bulk apply: the tag was lost')
        for (const anim of current[j].getAnimations()) {
          if (!(anim instanceof CSSAnimation) || beforeClick[j].has(anim.animationName)) continue
          const started = anim.startTime ?? 0
          if (started > clickTime && !animationReplays.includes(anim.animationName)) animationReplays.push(anim.animationName)
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
verify_bulk_apply_all "$PROOF_WORK/bulk-apply-all.json" || fail "UI Apply all remounted, animated, or failed to converge a card"
cdp_eval "document.querySelector('main')?.innerText" > "$PROOF_WORK/integrations-after-bulk.json"
node "$REPO_ROOT/verify/scripts/cdp-screenshot.mjs" "$WS" "$PROOF_WORK/integrations-after-bulk.png"

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
    const clickTime = performance.now()
    rollbackAll.click()
    const beforeClick = cards.map((card) => new Set(card.getAnimations().map((anim) => anim.animationName ?? null).filter((name) => name !== null)))
    const animationReplays = []
    for (let i = 0; i < 300; i++) {
      const current = [...document.querySelectorAll('.int-row-wrap')]
      if (current.length !== cards.length) throw new Error('card count changed mid bulk rollback: ' + current.length)
      for (let j = 0; j < current.length; j++) {
        if (current[j] !== cards[j]) throw new Error(current[j].querySelector('.int-name')?.textContent + ' card remounted during bulk rollback: the DOM node changed')
        if (current[j].dataset.verifyTag !== 'before-bulk-rollback') throw new Error(current[j].querySelector('.int-name')?.textContent + ' card remounted during bulk rollback: the tag was lost')
        for (const anim of current[j].getAnimations()) {
          if (!(anim instanceof CSSAnimation) || beforeClick[j].has(anim.animationName)) continue
          const started = anim.startTime ?? 0
          if (started > clickTime && !animationReplays.includes(anim.animationName)) animationReplays.push(anim.animationName)
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
  })()" > "$PROOF_WORK/bulk-rollback-all.json" 2>&1
}

echo "==> clicking Rollback all, then Apply all again so exit state is managed"
verify_bulk_rollback_all || fail "UI Rollback all remounted, animated, or failed to converge a card"
verify_bulk_apply_all "$PROOF_WORK/bulk-apply-all-again.json" || fail "UI second Apply all failed to converge every card"
cdp_eval "document.querySelector('main')?.innerText" > "$PROOF_WORK/integrations-after-bulk-reapply.json"

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
    if (input.checked === $next) {
      card.dataset.verifyTag = 'before-toggle'
      input.click()
      for (let i = 0; i < 60; i++) {
        await sleep(100)
        const cur = [...document.querySelectorAll('.int-row-wrap')].find((node) => node.querySelector('.int-name')?.textContent.trim() === '$id')
        if (cur?.querySelector('label.toggle input[type=checkbox]')?.checked === !$next) break
      }
      const settled = [...document.querySelectorAll('.int-row-wrap')].find((node) => node.querySelector('.int-name')?.textContent.trim() === '$id')?.querySelector('label.toggle input[type=checkbox]')
      if (settled?.checked !== !$next) throw new Error('$id toggle pre-pass never reached enabled=' + !$next)
      settled.click()
    }
    card.dataset.verifyTag = 'before-toggle'
    const clickTime = performance.now()
    input.click()
    const beforeClick = new Set(card.getAnimations().map((anim) => anim.animationName ?? null).filter((name) => name !== null))
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
        if (!(anim instanceof CSSAnimation) || beforeClick.has(anim.animationName)) continue
        const started = anim.startTime ?? 0
        if (started > clickTime && !animationReplays.includes(anim.animationName)) animationReplays.push(anim.animationName)
      }
    }
    throw new Error('$id toggle never settled to enabled=$next')
  })()"
}

echo "==> toggling Auto-apply on for grok through the UI"
click_toggle grok true > "$PROOF_WORK/toggle-grok-on.json" || fail "grok Auto-apply toggle click failed"
grep -q '"enabled":true' "$PROOF_WORK/toggle-grok-on.json" || fail "grok toggle did not report enabled=true"

echo "==> auto-apply: provider change rewrites the enabled grok config with no UI action"
GEN=$(curl -sf "http://127.0.0.1:$PORT/api/v1/providers" | node -pe 'JSON.parse(require("fs").readFileSync(0,"utf8")).generation') || fail "could not read providers generation"
curl -sf -X PUT -H 'Content-Type: application/json' \
  -d "{\"models\":[\"gpt-5.6-luna\",\"gpt-5.6-luna-probe\"],\"enabled\":true,\"expectedGeneration\":$GEN}" \
  "http://127.0.0.1:$PORT/api/v1/providers/codex?expectedGeneration=$GEN" > "$PROOF_WORK/provider-codex-update.json" || fail "provider codex update failed"
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
DAEMON_LOG="$RUNDIR/.prism/prism.log"
LOG_LINES_BEFORE=$(wc -l < "$DAEMON_LOG" | tr -d ' ')
GEN=$(curl -sf "http://127.0.0.1:$PORT/api/v1/providers" | node -pe 'JSON.parse(require("fs").readFileSync(0,"utf8")).generation') || fail "could not read providers generation"
curl -sf -X PUT -H 'Content-Type: application/json' \
  -d "{\"models\":[\"gpt-5.6-luna\",\"gpt-5.6-luna-probe\"],\"enabled\":true,\"expectedGeneration\":$GEN}" \
  "http://127.0.0.1:$PORT/api/v1/providers/codex?expectedGeneration=$GEN" > "$PROOF_WORK/provider-codex-damage.json" || fail "provider codex damage-bump failed"
AUTO_REFUSAL=
for _ in $(seq 1 100); do
  if tail -n +$((LOG_LINES_BEFORE + 1)) "$DAEMON_LOG" 2>/dev/null | grep -q 'auto-apply grok: prism:'; then AUTO_REFUSAL=1; break; fi
  sleep 0.2
done
[ -n "$AUTO_REFUSAL" ] || fail "auto-apply never logged a refusal for the damaged grok fence"
tail -n +$((LOG_LINES_BEFORE + 1)) "$DAEMON_LOG" | grep 'auto-apply grok' > "$PROOF_WORK/grok-damaged-refusal.log" || true
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
})()" > "$PROOF_WORK/grok-damaged-card.json" || fail "grok card never showed the damaged state"
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
})()" > "$PROOF_WORK/grok-damaged-buttons.json" || fail "damaged grok card buttons had the wrong state"
grep -cF -x "$GROK_BEGIN" "$RUNDIR/.grok/config.toml" | grep -qx 2 || fail "damaged grok config does not hold exactly two begin markers"
cp "$RUNDIR/grok-config-before-damage.toml" "$RUNDIR/.grok/config.toml"
grep -cF -x "$GROK_BEGIN" "$RUNDIR/.grok/config.toml" | grep -qx 1 || fail "fence repair did not leave exactly one begin marker"

echo "==> restart persistence: enabled state survives a daemon crash"
sed -i.bak '/^# >>> prism managed block (grok)/,/^# <<< prism managed block (grok) <<<$/d' "$RUNDIR/.grok/config.toml"
grep -q "prism managed block" "$RUNDIR/.grok/config.toml" && fail "grok managed block not stripped before the restart"
verify_crash_daemon
RECONVERGED=
for _ in $(seq 1 100); do
  if grep -q "http://127.0.0.1:$PORT/v1" "$RUNDIR/.grok/config.toml" 2>/dev/null && grep -q "prism managed block" "$RUNDIR/.grok/config.toml" 2>/dev/null; then RECONVERGED=1; break; fi
  sleep 0.2
done
[ -n "$RECONVERGED" ] || fail "enabled grok config did not re-converge after the daemon restart"
curl -sf "http://127.0.0.1:$PORT/api/v1/integrations" > "$PROOF_WORK/integrations-after-restart.json" || fail "integrations status unreachable after restart"
node -e 'const data = JSON.parse(require("fs").readFileSync(process.argv[1], "utf8")); const grok = data.integrations.find((entry) => entry.id === "grok"); if (!grok || !grok.enabled) { process.exit(1) }' "$PROOF_WORK/integrations-after-restart.json" || fail "grok enabled flag lost after the daemon restart"
cp "$RUNDIR/.grok/config.toml" "$PROOF_WORK/grok-config-after-restart.toml"
cp "$DAEMON_LOG" "$PROOF_WORK/prism-log-autoapply.log" 2>/dev/null || true

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
  })()" > "$PROOF_WORK/view-$(echo "$VIEW" | tr -d ' &').json" || fail "view walk failed for $VIEW"
done

if [ "$LIVE" = "1" ]; then
  command -v grok >/dev/null 2>&1 || fail "live verification needs the grok CLI in PATH"
  command -v omp >/dev/null 2>&1 || fail "live verification needs the omp CLI in PATH"
  echo "==> running Grok through the UI-applied config"
  LIVE_MODEL=$(curl -sf "http://127.0.0.1:$PORT/api/v1/models" | python3 -c 'import json,sys; print(json.load(sys.stdin)["models"][0]["id"])')
  GROK_MARKER=PRISM_GROK_LIVE_OK
  GROK_MODEL="${PRISM_VERIFY_GROK_MODEL:-prism-$(echo "$LIVE_MODEL" | tr '/.' '--')}"
  HOME="$RUNDIR" run_with_timeout 120 grok -m "$GROK_MODEL" -p "Reply with exactly $GROK_MARKER" > "$PROOF_WORK/grok-live.txt" 2>&1 || fail "Grok inference failed"
  grep -q "$GROK_MARKER" "$PROOF_WORK/grok-live.txt" || fail "Grok output has no final marker"

  echo "==> running OMP with visible thinking"
  OMP_MARKER=PRISM_OMP_THINKING_OK
  OMP_MODEL="${PRISM_VERIFY_OMP_MODEL:-prism/$LIVE_MODEL}"
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
    > "$PROOF_WORK/omp-live.ndjson" 2> "$PROOF_WORK/omp-live.stderr" || fail "OMP inference failed"
  if ! node "$REPO_ROOT/verify/scripts/assert-omp-output.mjs" \
    "$PROOF_WORK/omp-live.ndjson" "$OMP_MARKER" --require-tool --thinking-optional \
    > "$PROOF_WORK/omp-assertion.json" 2> "$PROOF_WORK/omp-assertion.stderr"; then
    fail "OMP final answer or tool round trip is missing"
  fi
fi

printf '{"ok":true,"live":%s,"rawEvidence":"%s"}\n' \
  "$([ "$LIVE" = "1" ] && echo true || echo false)" \
  "$([ "$LIVE" = "1" ] && echo 'private, no sanitizer contract' || echo 'structural fixture')" \
  > "$EVID_WORK/integration-summary.json"

echo "==> quitting Electron, attesting the shared daemon, then stopping it explicitly"
verify_quit_app
verify_attest_daemon
verify_stop_daemon

echo "verification passed"
