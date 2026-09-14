#!/bin/bash
set -euo pipefail

fail() {
  echo "FAIL: $1" >&2
  exit 1
}

REPO_ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd -P)
GO_ROOT="${GO_ROOT:-$REPO_ROOT}"
PORT="${PRISM_AGENTS_UI_PORT:-18798}"
CDP_PORT="${PRISM_CDP_PORT:-19223}"
if curl -sf --max-time 3 -o /dev/null "http://127.0.0.1:$PORT/api/v1/health" 2>/dev/null; then
  fail "a daemon is already bound to port $PORT; stop it before verification"
fi
RUNDIR=$(mktemp -d /tmp/prism-agentsui.XXXXXX)
EVID_WORK="$RUNDIR/evidence"
EVID_FINAL="${PRISM_VERIFY_EVIDENCE_DIR:-/tmp/prism-agentsui-evidence.$(date +%Y%m%d-%H%M%S).$$}"
mkdir -p "$EVID_WORK" "$RUNDIR/.prism" "$RUNDIR/sandbox/tools" "$RUNDIR/sandbox/home/.local/bin"

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
  cp -R "$EVID_WORK"/. "$EVID_FINAL"/
  rm -rf "$RUNDIR"
  echo "evidence: $EVID_FINAL"
  exit "$status"
}
trap cleanup EXIT INT TERM

for tool in bash sh; do
  printf '#!/bin/sh\nexit 0\n' > "$RUNDIR/sandbox/tools/$tool"
  chmod 755 "$RUNDIR/sandbox/tools/$tool"
done
NPM_PREFIX="$RUNDIR/sandbox/home/.local"
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

echo "==> building prismd and desktop"
(cd "$GO_ROOT" && go build -o "$RUNDIR/prismd" ./cmd/prismd) || fail "go build cmd/prismd"
npm run build --prefix "$REPO_ROOT" > "$RUNDIR/build.log" 2>&1 || fail "npm run build"

echo "==> launching isolated Electron with sandbox PATH"
SANDBOX_PATH="$RUNDIR/sandbox/tools:$RUNDIR/sandbox/home/.local/bin:/usr/bin:/bin"
PRISMD_PATH="$RUNDIR/prismd" \
PRISM_PORT="$PORT" \
PRISM_DAEMON_CONFIG="$RUNDIR/.prism/prism.json" \
PRISM_HEADLESS=1 \
PATH="$SANDBOX_PATH" \
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

echo "==> opening Integrations through the real UI"
cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
  const button = [...document.querySelectorAll('nav button')].find((node) => node.textContent.includes('Integrations'))
  if (!button) throw new Error('Integrations navigation button not found')
  button.click()
  for (let i = 0; i < 40; i++) {
    if (document.querySelector('main h1')?.textContent.trim() === 'Integrations' && document.querySelectorAll('.int-row-wrap').length >= 8) break
    await sleep(100)
  }
  if (document.querySelectorAll('.int-row-wrap').length < 8) throw new Error('integrations rows did not render')
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

node "$REPO_ROOT/verify/scripts/cdp-screenshot.mjs" "$WS" "$EVID_WORK/integrations-after.png"

echo "PASS: agents install/update UI drive"
