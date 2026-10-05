#!/bin/bash
set -euo pipefail

fail() {
  echo "FAIL: $1" >&2
  exit 1
}

REPO_ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)
GO_ROOT="${GO_ROOT:-$REPO_ROOT}"
PORT="${PRISM_AGENTS_UI_PORT:-18798}"
CDP_PORT="${PRISM_CDP_PORT:-19223}"
HEADLESS="${PRISM_HEADLESS:-1}"
source "$REPO_ROOT/verify/scripts/owned-runtime.sh"
verify_init agents-ui-drive "$PORT" "$CDP_PORT"
mkdir -p "$RUNDIR/sandbox/tools" "$RUNDIR/.local/bin"

for tool in bash sh; do
  printf '#!/bin/sh\nexit 0\n' > "$RUNDIR/sandbox/tools/$tool"
  chmod 755 "$RUNDIR/sandbox/tools/$tool"
done
NPM_PREFIX="$RUNDIR/.local"
mkdir -p "$NPM_PREFIX/bin"
cat > "$RUNDIR/sandbox/tools/npm" <<NPM
#!/bin/sh
if [ "\$1 \$2" = 'install -g' ]; then
  printf '#!/bin/sh\nif [ "\$1" = "--version" ]; then echo 0.0.0-prism-verify; exit 0; fi\nexit 0\n' > "$NPM_PREFIX/bin/codex"
  chmod 755 "$NPM_PREFIX/bin/codex"
  exit 0
fi
exit 1
NPM
chmod 755 "$RUNDIR/sandbox/tools/npm"

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
    "combos": {}, "routes": {}, "aliases": {}
  }
}
CONFIG
NODE_BIN=$(command -v node) || fail "node is required to launch electron"
ln -s "$NODE_BIN" "$RUNDIR/sandbox/tools/node"

echo "==> building prism and desktop"
(cd "$GO_ROOT" && go build -o "$RUNDIR/prism" ./cmd/prism) || fail "go build cmd/prism"
npm run build --prefix "$REPO_ROOT" > "$RUNDIR/build.log" 2>&1 || fail "npm run build"

echo "==> launching isolated Electron with sandbox PATH"
SANDBOX_PATH="$RUNDIR/sandbox/tools:$RUNDIR/.local/bin:/usr/bin:/bin"
VERIFY_PATH="$SANDBOX_PATH"
VERIFY_SHELL="$RUNDIR/sandbox/login-shell"
printf '#!/bin/sh\nprintf "%%s" '\''%s'\''\n' "$SANDBOX_PATH" > "$VERIFY_SHELL"
chmod 755 "$VERIFY_SHELL"
[ "$(env -i "$VERIFY_SHELL" -ilc 'printf "%s" "$PATH"')" = "$SANDBOX_PATH" ] || fail "login shell fixture did not return the sandbox PATH with an empty environment"
verify_start_electron
curl -sf --max-time 5 "http://127.0.0.1:$PORT/api/v1/health" > "$EVID_WORK/health.json" || fail "daemon health failed"

cdp_eval() {
  node "$REPO_ROOT/verify/scripts/cdp-eval.mjs" "$WS" "$1" "${2:-30000}"
}

echo "==> enabling Other agents and Agent install and update through the real UI"
cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
  const nav = [...document.querySelectorAll('nav button')].find((node) => node.textContent.includes('Experimental'))
  if (!nav) throw new Error('Experimental navigation button not found')
  nav.click()
  for (let i = 0; i < 40; i++) {
    if (document.querySelector('main h1')?.textContent.trim() === 'Experimental') break
    await sleep(100)
  }
  for (const label of ['Other agents', 'Agent install and update']) {
    let input = null
    for (let i = 0; i < 40 && !input; i++) {
      input = [...document.querySelectorAll('.experimental-flag')].find((node) => node.textContent.includes(label))?.querySelector('input')
      if (!input) await sleep(100)
    }
    if (!input) throw new Error(label + ' toggle not found')
    if (!input.checked) input.click()
    await sleep(200)
    if (!input.checked) throw new Error(label + ' did not switch on')
  }
  return {enabled: true}
})()" > "$EVID_WORK/experimental-flags.json" || fail "could not enable experimental flags through UI"

echo "==> opening Integrations through the real UI"
cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
  const button = [...document.querySelectorAll('nav button')].find((node) => node.textContent.includes('Integrations'))
  if (!button) throw new Error('Integrations navigation button not found')
  button.click()
  for (let i = 0; i < 40; i++) {
    if (document.querySelector('main h1')?.textContent.trim() === 'Integrations' && document.querySelectorAll('.int-row-wrap').length >= 7) break
    await sleep(100)
  }
  if (document.querySelectorAll('.int-row-wrap').length < 7) throw new Error('integrations rows did not render')
  const cell = [...document.querySelectorAll('.int-install')][0]
  if (!cell) throw new Error('install cell missing from rows')
  return {cells: document.querySelectorAll('.int-install').length, first: cell.innerText}
})()" > "$EVID_WORK/ui-before.json" || { cdp_eval "document.querySelector('main')?.innerText" > "$EVID_WORK/failure-text.json" 2>&1 || true; fail "install cells did not render"; }

echo "==> clicking Install on the codex card"
cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
  const card = [...document.querySelectorAll('.int-row-wrap')].find((node) => node.querySelector('.int-name')?.textContent.trim() === 'codex')
  if (!card) throw new Error('codex card not found')
  const button = [...card.querySelectorAll('.int-install button')].find((node) => node.textContent.trim() === 'Install')
  if (!button) throw new Error('codex Install button not found; card text: ' + card.innerText)
  if (button.disabled) throw new Error('codex Install button is disabled; card text: ' + card.innerText)
  button.click()
  for (let i = 0; i < 160; i++) {
    await sleep(150)
    const current = [...document.querySelectorAll('.int-row-wrap')].find((node) => node.querySelector('.int-name')?.textContent.trim() === 'codex')
    const alert = current?.querySelector('[role=alert]')
    if (alert) throw new Error('install alert: ' + alert.textContent)
    const state = current?.querySelector('.int-install__state')?.textContent.trim()
    const actions = current?.querySelector('.int-install__actions')?.textContent.trim()
    if (state === 'installed' || (state === 'script' && actions?.includes('Reinstall'))) {
      return {id: 'codex', state, text: current?.innerText}
    }
    if (state === 'failed' || state === 'unsupported' || state === 'interrupted') {
      throw new Error('install ended in ' + state + ': ' + (current?.innerText ?? ''))
    }
  }
  throw new Error('install never settled; card: ' + (card.innerText))
})()" 30000 > "$EVID_WORK/ui-install-codex.json" || { cdp_eval "document.querySelector('main')?.innerText" > "$EVID_WORK/failure-text.json" 2>&1 || true; fail "UI Install failed for codex"; }

echo "==> assert install flowed through the real chain"
curl -sf "http://127.0.0.1:$PORT/api/v1/agents/codex/job" > "$EVID_WORK/daemon-job.json" || fail "daemon job endpoint failed"
grep -q '"state":"succeeded"' "$EVID_WORK/daemon-job.json" || fail "daemon job is not succeeded after UI install"
grep -q 'npm install -g @openai/codex' "$EVID_WORK/daemon-job.json" || fail "daemon job did not run the npm plan"
curl -sf "http://127.0.0.1:$PORT/api/v1/agents/codex" > "$EVID_WORK/daemon-status-codex.json" || fail "daemon agent status failed"
grep -q '"installed":true' "$EVID_WORK/daemon-status-codex.json" || fail "daemon does not report codex as installed after UI install"
[ -x "$RUNDIR/.local/bin/codex" ] || fail "install did not create codex under the isolated HOME"
[ "$("$RUNDIR/.local/bin/codex" --version)" = "0.0.0-prism-verify" ] || fail "installed fixture version differs"

node "$REPO_ROOT/verify/scripts/cdp-screenshot.mjs" "$WS" "$EVID_WORK/integrations-after.png"

echo "==> quitting Electron, attesting the shared daemon, then stopping it explicitly"
verify_quit_app
verify_attest_daemon
verify_stop_daemon

echo "PASS: agents install/update UI drive"
