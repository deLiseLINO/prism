#!/bin/bash
set -euo pipefail

fail() {
  echo "FAIL: $1" >&2
  exit 1
}

REPO_ROOT=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)
GO_ROOT="${GO_ROOT:-$REPO_ROOT}"
PY3="${PYTHON:-}"
if [ -z "$PY3" ]; then
  { for cand in "$(command -v python3 2>/dev/null)" /usr/bin/python3; do
      [ -n "$cand" ] || continue
      ( "$cand" -c 'import json' ) >/dev/null 2>&1 &
      if wait $!; then PY3="$cand"; break; fi
    done; } 2>/dev/null
fi
[ -n "$PY3" ] || fail "no working python3 found"
PORT="${PRISM_PORT:-18787}"
CDP_PORT="${PRISM_CDP_PORT:-19222}"
HEADLESS="${PRISM_HEADLESS:-1}"

hash256() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$@"
  elif command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$@"
  else
    openssl dgst -sha256 -r "$@"
  fi
}

SOURCE_STATE="${PRISM_VERIFY_STATE_DIR:-$HOME/.prism}"
source "$REPO_ROOT/verify/scripts/owned-runtime.sh"
verify_init quota-proof "$PORT" "$CDP_PORT"
VERIFY_LIVE=1
verify_stage_state "$SOURCE_STATE"
PRIVATE_WORK="$RUNDIR/private-proof"
mkdir -p "$PRIVATE_WORK" "$RUNDIR/.codex" "$RUNDIR/.grok" "$RUNDIR/.omp/agent"

echo "==> building prism and desktop"
(cd "$GO_ROOT" && go build -o "$RUNDIR/prism" ./cmd/prism) || fail "go build cmd/prism"
npm run build --prefix "$REPO_ROOT" > "$RUNDIR/build.log" 2>&1 || fail "npm run build"
echo "==> launching isolated Electron against a copy of real accounts"
verify_start_electron
curl -sf --max-time 5 "http://127.0.0.1:$PORT/api/v1/health" > "$EVID_WORK/health.json" || fail "daemon health failed"

cdp_eval() {
  node "$REPO_ROOT/verify/scripts/cdp-eval.mjs" "$WS" "$1" "${2:-30000}"
}

curl -sf "http://127.0.0.1:$PORT/api/v1/accounts" > "$PRIVATE_WORK/accounts.json" || fail "accounts list failed"
curl -sf "http://127.0.0.1:$PORT/api/v1/providers" > "$PRIVATE_WORK/providers.json" || fail "providers list failed"

while IFS=$'\t' read -r account_id encoded_id wire; do
  [ -n "$account_id" ] || continue
  if [ "$wire" = codex ] || [ "$wire" = antigravity ]; then
    curl -sf "http://127.0.0.1:$PORT/api/v1/accounts/$encoded_id/quota" \
      > "$PRIVATE_WORK/quota-$encoded_id.json" \
      || fail "live quota endpoint failed for $account_id ($wire)"
  fi
done < <("$PY3" - "$PRIVATE_WORK" <<'QUOTA_IDS'
import json, sys, urllib.parse
evid = sys.argv[1]
accounts = json.load(open(f'{evid}/accounts.json'))['accounts']
providers = json.load(open(f'{evid}/providers.json'))['providers']
wires = {provider['id']: provider['wire'] for provider in providers}
for account in accounts:
    account_id = account['id']
    print(f"{account_id}\t{urllib.parse.quote(account_id, safe='')}\t{wires.get(account['provider'], '')}")
QUOTA_IDS
)
N_ACCOUNTS=$("$PY3" -c 'import json; print(len(json.load(open("'"$PRIVATE_WORK"'/accounts.json"))["accounts"]))')
[ "$N_ACCOUNTS" -gt 0 ] || fail "no accounts present; quota proof needs at least one real account"

echo "==> reading Accounts UI cards"
cdp_eval "await (async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
  const button = [...document.querySelectorAll('nav button')].find((node) => node.textContent.includes('Accounts'))
  if (!button) throw new Error('Accounts navigation button not found')
  button.click()
  for (let i = 0; i < 40; i++) {
    if (document.querySelector('main h1')?.textContent.trim() === 'Accounts') break
    await sleep(100)
  }
  if (document.querySelector('main h1')?.textContent.trim() !== 'Accounts') throw new Error('Accounts view did not open')
  for (let i = 0; i < 80; i++) {
    const cards = [...document.querySelectorAll('main article.usage-card')]
    const settled = cards.length > 0 && cards.every((card) => !card.querySelector('.skel'))
    if (settled) break
    await sleep(250)
  }
  const cards = [...document.querySelectorAll('main article.usage-card')].map((card) => ({
    name: card.querySelector('.usage-card-name span[title]')?.getAttribute('title') ?? '',
    provider: card.querySelector('.usage-card-prov')?.textContent.trim() ?? '',
    state: card.querySelector('.usage-card-badges .badge')?.textContent.trim() ?? '',
    windows: [...card.querySelectorAll('.usage-window')].map((win) => ({
      label: win.querySelector('.usage-window-label')?.textContent.trim() ?? '',
      percent: win.querySelector('.usage-window-val')?.textContent.trim() ?? '',
      reset: win.querySelector('.usage-window-reset')?.textContent.trim() ?? '',
    })),
  }))
  if (cards.length === 0) throw new Error('no usage cards rendered')
  return {cards}
})()" > "$PRIVATE_WORK/accounts-ui-cards.json" || fail "could not read Accounts view cards through the UI"

node "$REPO_ROOT/verify/scripts/cdp-screenshot.mjs" "$WS" "$PRIVATE_WORK/accounts.png"

echo "==> matching management API responses"
curl -sf "http://127.0.0.1:$PORT/api/v1/usage" > "$PRIVATE_WORK/usage.json" || fail "usage endpoint failed"
"$PY3" - "$PRIVATE_WORK" <<'MATCH'
import json, pathlib, sys, urllib.parse
evid = pathlib.Path(sys.argv[1])
accounts = json.load(open(evid / 'accounts.json'))['accounts']
providers = json.load(open(evid / 'providers.json'))['providers']
usage = json.load(open(evid / 'usage.json'))['accounts']
ui_cards = json.load(open(evid / 'accounts-ui-cards.json'))['cards']
wires = {provider['id']: provider['wire'] for provider in providers}

present_wires = {wires.get(account['provider']) for account in accounts}
missing = {'codex', 'antigravity'} - present_wires
if missing:
    raise SystemExit(f'quota proof needs accounts for both live quota wires; missing: {sorted(missing)}')

STATE_LABELS = {'cooling_down': 'cooling down', 'needs_reauth': 'needs reauth', 'soft_avoid': 'soft avoid'}

def card_for(account_id, email):
    names = {account_id, email or ''}
    return next((card for card in ui_cards if card['name'] in names), None)

findings = []
displayed = 0
for account in accounts:
    account_id = account['id']
    wire = wires.get(account['provider'])
    usage_row = next((item for item in usage if item['account'] == account_id), None)
    if usage_row is None:
        raise SystemExit(f'account {account_id} missing from /api/v1/usage')
    card = card_for(account_id, usage_row.get('email'))

    live_wire = account['provider'] in {'codex', 'antigravity'}
    if wire in {'codex', 'antigravity'}:
        encoded = urllib.parse.quote(account_id, safe='')
        quota = json.load(open(evid / f'quota-{encoded}.json'))['quota']
        state = 'available' if quota.get('source', 'unknown') != 'unknown' else 'unavailable'
    else:
        quota = account.get('quota') or {}
        state = 'unavailable' if quota.get('limit') is None else 'available'
    windows = quota.get('windows') or []

    if live_wire or (quota.get('source', 'unknown') != 'unknown' and windows):
        if card is None:
            raise SystemExit(f'account {account_id} not visible in Accounts UI cards')
        displayed += 1
        if card['provider'] != account['provider']:
            raise SystemExit(f"account {account_id} card provider {card['provider']!r}, expected {account['provider']!r}")
        expected_state = STATE_LABELS.get(account['state'], account['state'])
        if card['state'] != expected_state:
            raise SystemExit(f"account {account_id} card state {card['state']!r}, expected {expected_state!r}")
        if quota.get('source', 'unknown') != 'unknown' and windows:
            if len(card['windows']) != len(windows):
                raise SystemExit(f'account {account_id} card shows {len(card["windows"])} windows, API has {len(windows)}')
            def percent(window):
                left = 0 if window.get('limit') in (None, 0) else max(0, min(1, 1 - window['used'] / window['limit']))
                return f'{round(left * 100)}%'
            api_percents = sorted(percent(window) for window in windows)
            card_percents = sorted(ui_window['percent'] for ui_window in card['windows'])
            if api_percents != card_percents:
                raise SystemExit(f'account {account_id} window percents {card_percents} != API {api_percents}')
    elif card is not None:
        displayed += 1

    findings.append({
        'account': account_id, 'provider': account['provider'], 'wire': wire,
        'quotaState': state, 'liveQuota': quota,
        'uiWindows': len(card['windows']) if card else 0, 'apiUsage': usage_row,
    })

if len(ui_cards) != displayed:
    raise SystemExit(f'Accounts UI has {len(ui_cards)} cards but expected {displayed}')
json.dump({'ok': True, 'accounts': findings}, open(evid / 'quota-match.json', 'w'), indent=2)
print(f'matched {len(findings)} account(s) by provider wire')
MATCH

curl -sf "http://127.0.0.1:$PORT/api/v1/accounts" > "$PRIVATE_WORK/accounts-after.json" || fail "post-read accounts snapshot failed"
if ! "$PY3" - "$PRIVATE_WORK" <<'MUTATION'
import json, sys
evid = sys.argv[1]
VOLATILE = {'quota', 'credentialGeneration'}
def strip_volatile(name):
    doc = json.load(open(f'{evid}/accounts.json' if name == 'before' else f'{evid}/accounts-after.json'))
    return [{k: v for k, v in a.items() if k not in VOLATILE} for a in doc['accounts']]
before = strip_volatile('before')
after = strip_volatile('after')
if before != after:
    print(f'account fields changed during quota proof:\nbefore={before}\nafter={after}', file=sys.stderr)
    raise SystemExit(1)
MUTATION
then
  fail "accounts state changed during quota proof (mutation detected)"
fi

"$PY3" - "$PRIVATE_WORK/quota-match.json" "$EVID_WORK/quota-summary.json" <<'SUMMARY'
import json, sys
result = json.load(open(sys.argv[1]))
json.dump({
    'ok': result['ok'],
    'matchedAccounts': len(result['accounts']),
    'accountMutationCheck': 'passed',
    'rawEvidence': 'private, no sanitizer contract',
}, open(sys.argv[2], 'w'), indent=2)
SUMMARY

echo "==> quitting Electron, attesting the shared daemon, then stopping it explicitly"
verify_quit_app
verify_attest_daemon
verify_stop_daemon

echo "quota proof passed (read-only; no account mutated)"
