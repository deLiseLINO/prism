# Usage

The Usage view renders the real per-account quota snapshot decoded by Prism: account, provider, state, used/limit, window end, and source. It is the pool-wide read a user checks before a long run.

## Sub-features

- Read-only table from `GET /api/v1/usage` (the daemon pool snapshot), sorted by account.
- Quota bar per row: fill at `min(100, round(used/limit*100))%`, meta `used` alone when limit is null, `used / limit` otherwise.
- State badges mirror the accounts view tones (`active` ok, `paused` muted, `needs_reauth` error, `cooling_down`/`soft_avoid` warn).
- Empty state: `No usage reported.`.
- Search over account/provider/state and `{filtered} of {total} shown` counter.

## How to get to it (user POV)

Click `Usage` (`#/usage`). The table lists every account with its consumption and reset window.

## Driving it with the harness

`scripts/quota-proof.sh` covers this view read-only: it captures the Usage rows through the UI, matches account, provider, state, used/limit, window end, and source against `GET /api/v1/usage`, rejects extra or missing rows, and cross-matches the Accounts view. Separately, `live-matrix.sh` records `daemon/usage-before.json` and `daemon/usage-after.json` around inference.

For a focused drive: navigate to `#/usage` over CDP and diff the rendered rows against the API payload. Every account in `/api/v1/accounts` must appear here.

## Gotchas

- Usage is a snapshot of the pool at read time, not an accounting history: it resets with the daemon and shows the current window, nothing else.
- A null limit renders `used` with no denominator — that is the precise unavailable state, not a bug.
- Codex quota comes from response headers (`source: header`); antigravity from the models endpoint payload (`source: endpoint`). Different providers legitimately show different sources in the same table.
- `windowEnd` is the upstream reset time; it can be null/zero before any inference has flowed.
