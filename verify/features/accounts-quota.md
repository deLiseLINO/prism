# Accounts and quota

The Accounts view lists stored accounts for providers with quota windows. Today that is `codex` and `antigravity`. Other providers stay hidden. It marks the account in use, offers explicit selection when needed, and warns if a selected account was deleted. The daemon pool snapshot backs the list; provider settings back the selection.

## Sub-features

- Account rows: id, state badge, quota windows, Use this account, and Remove actions. A single unpinned account is marked In use; a provider with multiple unpinned accounts prompts for selection.
- Quota rendering: codex/antigravity accounts probe `GET /api/v1/accounts/{id}/quota` live — `unknown` source renders exactly `quota unavailable`, ready values render `used / limit` (`?` when limit is missing) with a fill bar at `min(100, round(used/limit*100))%` plus `window ends … · source …` telemetry; other wires render the accounts-list snapshot — a null limit renders exactly `no quota`, a zero limit renders `used / ?`, otherwise `used / limit` with the same fill bar. The quota object carries `used`, `limit` (nullable), `windowEnd`, and `source` (`header`, `endpoint`, `report`, `probe`, `unknown`).
- Mutations: pause/resume/priority POST `{version}` under CAS — stale `version` returns 409 `stale_version` and renders a `Re-fetch account` button; remove is a DELETE with no version.
- Selection: native providers can store several accounts, but requests use only `pinnedAccount`. With one stored account and no pin, that account is used implicitly, including a custom provider with one `:default` account. With more than one, select an account before routing. A deleted selected account leaves requests blocked until another is selected; the Usage view shows the missing selection. Changing the selection writes the full provider under generation CAS. There is no automatic account rotation. Combo failover between provider targets remains available.
- Search: Accounts filters by account, email, provider, or state.
- Usage data: account quota snapshots are displayed on each account card; live quota refresh is available on supported wires.
- Backing endpoints: `GET /api/v1/accounts`, `POST /api/v1/accounts/{id}/pause|resume|priority`, `DELETE /api/v1/accounts/{id}`, `GET /api/v1/accounts/{id}/quota`, `GET /api/v1/usage`.
- Quota sources: codex quota decodes `x-codex-*` response headers (source `header`); antigravity quota decodes the models endpoint payload (source `endpoint`). `GET /api/v1/accounts/{id}/quota` also probes the provider actively when its stored snapshot is stale: codex via `GET https://chatgpt.com/backend-api/wham/usage` (Bearer access token + `ChatGPT-Account-Id`), antigravity via `POST {baseURL}/v1internal:fetchAvailableModels` with the credential's project and the IDE user agent. Probes are TTL-cached for 5 minutes per account, record the result into the pool, and a failed probe keeps the last known snapshot — the caller never sees fabricated data. Snapshots use basis points across both wires: `used` 0–10000 with `limit` 10000 (70% is `7000 / 10000`).

## How to get to it (user POV)

Click `Accounts` (`#/usage`) to view account cards and select the account used for requests.

## Driving it with the harness

Read-only proof (required for any quota rendering change):

```sh
verify/scripts/quota-proof.sh
```

It copies the real `~/.prism` state into a sandbox daemon, snapshots accounts and providers, resolves provider IDs to wires, fetches live per-account quota for codex/antigravity, reads both views through the UI, and matches Accounts plus every Usage column against the relevant API. It re-hashes the accounts snapshot to prove nothing was mutated. It fails if either live wire has no account.

Mutation paths are proven by `scripts/prismctl-proof.sh` in the sandbox: `accounts select` pins a stored account when one exists, and `accounts pause/resume/priority/quota/remove` run against the isolated daemon when an account exists there. Never drive Remove during verification against the user's real accounts. A delete-path change is proven in the sandbox only. See management-views.md for the bridge drive recipe and the CAS gotchas.

## Gotchas

- Rate limits, quota failures, and server or transport errors keep the selected account available for the next request. The upstream error is returned to the caller. A rejected credential still requires reauthorization.
- The quota `limit` is nullable, not zero-default: `no quota` (null) and `0` (`used / ?`) are different states. The proof asserts the exact string.
- `GET /api/v1/accounts/{id}/quota` backs the codex/antigravity live quota cells and the prismctl `accounts quota` command; other wires render the accounts-list snapshot.
- Selection writes are full provider replaces: the CLI and UI retain `accountsPath` while changing `pinnedAccount`. A partial write can silently drop provider settings.
- Quota values exist after inference has flowed or an active probe ran: on the live wires the quota endpoint probes on demand (5-minute TTL), so a fresh codex/antigravity account shows real numbers on first read. `quota unavailable` now means the probe could not answer — most commonly a missing credential blob (the daemon log names the account and reason); such an account needs a re-login before quota or inference can flow.
