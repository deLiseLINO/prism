# Usage (Accounts)

The `#/usage` route is the Accounts view. It renders one card per account for the providers with live quota (codex and antigravity), using the pool snapshot decoded by Prism. It is where a user checks remaining quota, picks the account in use, adds an account, or removes one.

## Sub-features

- Card grid (`.usage-grid` of `article.usage-card`) built from `GET /api/v1/usage`, filtered in the renderer to codex and antigravity accounts. The endpoint itself still returns every account, sorted by account.
- Card header shows a state dot, the email (falls back to the account id), a state badge, and an `In use` badge on the pinned account or the sole implicit account. Provider name sits under the header.
- Per-window bars show percent left, `round((1 - used/limit) * 100)`, clamped to 0..100, plus the reset time. A missing or zero limit renders 0%.
- Quota windows load per account from `GET /api/v1/accounts/{id}/quota`. Loads run 3 at a time with up to 3 attempts and refresh in the background every 300 s. While loading, the card shows skeleton lines. With no windows it shows `Quota unavailable` or `Loading…`.
- State badge tones. `active` ok, `paused` muted, `needs_reauth` error, `cooling_down` and `soft_avoid` warn.
- Head actions. `Add account` opens the provider login flow. `Refresh all` forces a quota refresh for every card. A search box filters by account, email, provider, or state.
- Card actions. `Use this account` pins the account through a provider write. It hides on the pinned account, on the sole implicit account, and when the provider is unknown. `Remove` opens a confirm (`Remove <account>?`) and deletes the account.
- Alerts. `Selected account <id> for <provider> is missing…` when the pin points at no account. `<provider> has multiple accounts. Choose one to enable requests.` when no pin is set.
- Empty states. `No accounts yet. Add one to start routing.` and `No accounts match the filter.`.

## How to get to it (user POV)

Click `Accounts` in the nav (`#/usage`, heading `Accounts`).

## Driving it with the harness

`scripts/quota-proof.sh` drives the Accounts cards over CDP, matches each card to `GET /api/v1/usage`, and rejects extra or missing cards. `live-matrix.sh` records `daemon/usage-before.json` and `daemon/usage-after.json` around inference.

For a focused drive, navigate to `#/usage`, read the `.usage-card` elements, and diff them against the API payload after dropping providers other than codex and antigravity. Accounts of other providers appear in `/api/v1/usage` and `/api/v1/accounts` but never as cards.

## Gotchas

- The card set is a subset of the API. Do not expect one card per `/api/v1/usage` row.
- The view is not read-only. `Use this account` and `Remove` mutate the pool, so guard them in read-only proofs.
- Usage is a snapshot of the pool at read time, not an accounting history. It resets with the daemon.
- Bars show remaining quota, not consumption. Compare `100 - percent` against used/limit from the API.
- Codex quota comes from response headers (`source: header`) or the usage endpoint. Antigravity comes from the models endpoint (`source: endpoint`).
- `windowEnd` is the upstream reset time and can be null or zero before any inference has flowed.
