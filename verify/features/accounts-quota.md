# Accounts and quota

The Accounts view is a card grid of stored accounts for providers with quota windows. Today that is `codex` and `antigravity`. Other providers stay hidden. It marks the account in use, offers explicit selection when needed, and warns if a selected account was deleted. The grid is fed by `GET /api/v1/usage` plus `GET /api/v1/providers`; per-account quota comes from `GET /api/v1/accounts/{id}/quota`.

## Sub-features

- Cards: each account is an `article.usage-card` inside `div.usage-grid`. There are no table rows and no priority or cooldown cells. A card shows the account name (`.usage-card-name span[title]`), provider (`.usage-card-prov`), state badge and an `In use` badge when the account is pinned or the sole implicit one. Each card has `Use this account` and `Remove`.
- Header controls: `Add account` opens the add-account modal. `Refresh all` forces a quota probe for every account and is disabled when there are no accounts. Search input `#usage-search` has the placeholder `Filter by account, email, provider, or state`. Empty states are `No accounts yet. Add one to start routing.` and `No accounts match the filter.` There is no filtered/total counter.
- Quota rendering: one bar per entry in `quota.windows[]` (`.usage-window`). The header (`-label`) is `5 hour`, `Weekly`, `5h gemini` or `weekly claude`. The value (`-val`) is `NN%` left, `round(clamp(1 - used/limit) * 100)`, and `0%` when limit is missing or 0. The bar fill is the left ratio. The reset text (`-reset`) is `Resets <time> (<remaining>)`, `Resets now` or `Resets unknown`. Snapshots use basis points (`used` 0 to 10000, `limit` 10000), so `7000 / 10000` renders `30%`.
- Empty and failed states: with no windows, the card shows two skeleton lines (`.skel`) and `Loading…` until the load fails. It then shows `Quota unavailable`. A failure is recorded after 3 attempts, or at once on a manual refresh. Cards re-poll every 300s with 3 concurrent loads.
- Which providers appear: only provider ids `codex` and `antigravity` (not wire based).
- Selection: native providers can store several accounts, but requests use only `pinnedAccount`. With one stored account and no pin, that account is used implicitly, including a custom provider with one `:default` account. With more than one, select an account before routing. `Use this account` is hidden when the account is already pinned or is the sole implicit one. The Accounts view itself shows the alerts `Selected account X for P is missing. Choose another account to resume requests.` and `P has multiple accounts. Choose one to enable requests.` A deleted selected account leaves requests blocked until another is selected. Changing the selection writes the full provider (`accountsPath`, `pinnedAccount`) under generation CAS. There is no automatic account rotation. Combo failover between provider targets remains available.
- Remove: opens an inline confirm `Remove <label>?` and a second `Remove` confirms. The detail is `Requests will stop until you select another account.` when pinned, `Requests will stop until you add another account.` when implicit, and `The account leaves the pool immediately.` otherwise. It sends DELETE with no version, returns 204 and deletes the durable credential.
- Pause, resume and priority have no UI controls. They are API and prismctl only. They POST `{version}` under CAS, and a stale `version` returns 409 `stale_version`.
- Backing endpoints: `GET /api/v1/usage`, `GET /api/v1/providers`, `GET /api/v1/accounts` (used by the proof and prismctl), `POST /api/v1/accounts/{id}/pause|resume|priority`, `DELETE /api/v1/accounts/{id}`, `GET /api/v1/accounts/{id}/quota`, `POST /api/v1/accounts/{id}/quota/refresh`.
- Quota API: the quota object carries `used`, `limit` (nullable), `windowEnd`, `source` (`header`, `endpoint`, `report`, `probe`, `unknown`) and `windows[]`. The null limit exists in the API only and has no UI expression. The refresh route forces a probe with a 30s timeout and returns 502 `quota_refresh_failed` on error instead of a stale snapshot.
- Quota sources: codex probes `GET https://chatgpt.com/backend-api/wham/usage` (Bearer access token plus `ChatGPT-Account-Id`, 8s timeout). Antigravity probes `POST {baseURL}/v1internal:retrieveUserQuotaSummary`, and falls back to `fetchAvailableModels` only when the summary has no windows. The governing window is the highest-used one, and the per-window list is stored. The first read always probes. After that the TTL is 5 minutes since the last probe per account. Probes record into the pool. On a non-forced read a failed probe keeps the last known snapshot. Nothing is fabricated.

## How to get to it (user POV)

Click `Accounts` (`#/usage`) to view account cards and select the account used for requests.

## Driving it with the harness

Read-only proof (required for any quota rendering change):

```sh
verify/scripts/quota-proof.sh
```

It needs `~/.prism` with accounts on both the codex and antigravity wires, and it refuses to run if a daemon is bound to port 18787. It copies the real state into a sandbox daemon, snapshots accounts and providers, fetches live per-account quota, and reads the Accounts cards only. It never opens Usage. Per card it reads name, provider, first badge, and each window label, value and reset. It compares provider, state label (cooling down, needs reauth, soft avoid), window count and the sorted `NN%` left values against the API windows. It asserts the card count equals the expected displayed count. It compares the before and after account fields directly, excluding `quota` and `credentialGeneration`. It fails if either live wire has no account.

By hand:

1. Click `Accounts` (`#/usage`).
2. Wait until no `main article.usage-card` has a `.skel`.
3. Per card read `.usage-card-name span[title]`, `.usage-card-prov`, `.usage-card-badges .badge`, and each `.usage-window` `-label`, `-val`, `-reset`.
4. Compare with `GET /api/v1/accounts/{id}/quota` `quota.windows`.
5. Type in `#usage-search` to filter.
6. `Refresh all` triggers the quota refresh route per account.

The CLI script exercises account mutations only when the sandbox contains a stored account. Its normal empty-account fixture records these actions as skipped. `accounts select` pins a stored account when one exists, and `accounts pause/resume/priority/quota/remove` run against the isolated daemon when an account exists there. Never drive Remove during verification against the user's real accounts. A delete-path change is proven in the sandbox only. See management-views.md for the bridge drive recipe and the CAS gotchas.

## Gotchas

- Rate limits, quota failures, and server or transport errors keep the selected account available for the next request. The upstream error is returned to the caller. A rejected credential still requires reauthorization.
- The UI never renders `used / limit`, `no quota` or `quota unavailable`. Assert `NN%` left per window and `Quota unavailable` (capital Q) only.
- `GET /api/v1/accounts/{id}/quota` backs the card bars and the prismctl `accounts quota` command.
- Selection writes are full provider replaces. The CLI and UI retain `accountsPath` while changing `pinnedAccount`. A partial write can silently drop provider settings.
- A fresh account shows real numbers on first read because the endpoint probes on demand. `Loading…` shows until 3 failed attempts, then `Quota unavailable`. That means the probe could not answer, most commonly a missing credential blob (the daemon log names the account and reason). Such an account needs a re-login before quota or inference can flow.
