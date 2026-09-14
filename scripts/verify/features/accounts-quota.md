# Accounts and quota

The Accounts view groups accounts by provider and renders each account's state, priority, quota, cooldown, and actions; a per-provider selection policy card sits above each table. The Usage view renders the same quota data pool-wide. Both are backed by the daemon pool snapshot.

## Sub-features

- Account rows: id, state badge (`active`, `paused`, `needs_reauth`, `cooling_down`, `soft_avoid`), priority input with Save, quota cell, cooldown timestamp, and Pause/Resume/Remove actions.
- Quota rendering: codex/antigravity accounts probe `GET /api/v1/accounts/{id}/quota` live — `unknown` source renders exactly `quota unavailable`, ready values render `used / limit` (`?` when limit is missing) with a fill bar at `min(100, round(used/limit*100))%` plus `window ends … · source …` telemetry; other wires render the accounts-list snapshot — a null limit renders exactly `no quota`, a zero limit renders `used / ?`, otherwise `used / limit` with the same fill bar. The quota object carries `used`, `limit` (nullable), `windowEnd`, and `source` (`header`, `endpoint`, `report`, `probe`, `unknown`).
- Mutations: pause/resume/priority POST `{version}` under CAS — stale `version` returns 409 `stale_version` and renders a `Re-fetch account` button; remove is a DELETE with no version.
- Selection policy: per provider — strategy (`quota`, `round_robin`, `fill_first`), affinity (`sticky`, `off`), auto-switch threshold (0–1), pinned account, auto-switch toggle; saved via a full provider replace under generation CAS.
- Search and counters: Accounts filters account/provider/state; Usage filters account/provider/state; both show the visible/total count.
- Usage view: read-only table of account, provider, state, used/limit, window end, and source. A null limit renders the used value without a denominator.
- Backing endpoints: `GET /api/v1/accounts`, `POST /api/v1/accounts/{id}/pause|resume|priority`, `DELETE /api/v1/accounts/{id}`, `GET /api/v1/accounts/{id}/quota`, `GET /api/v1/usage`.
- Quota sources: codex quota decodes `x-codex-*` response headers (source `header`); antigravity quota decodes the models endpoint payload (source `endpoint`). `GET /api/v1/accounts/{id}/quota` also probes the provider actively when its stored snapshot is stale: codex via `GET https://chatgpt.com/backend-api/wham/usage` (Bearer access token + `ChatGPT-Account-Id`), antigravity via `POST {baseURL}/v1internal:fetchAvailableModels` with the credential's project and the IDE user agent. Probes are TTL-cached for 5 minutes per account, record the result into the pool, and a failed probe keeps the last known snapshot — the caller never sees fabricated data. Snapshots use basis points across both wires: `used` 0–10000 with `limit` 10000 (70% is `7000 / 10000`).

## How to get to it (user POV)

Click `Accounts` (`#/accounts`) for the tables and policy cards; click `Usage` (`#/usage`) for the pool-wide quota readout. The quota column shows the live window consumption per account.

## Driving it with the harness

Read-only proof (required for any quota rendering change):

```sh
scripts/verify/scripts/quota-proof.sh
```

It copies the real `~/.prism` state into a sandbox daemon, snapshots accounts and providers, resolves provider IDs to wires, fetches live per-account quota for codex/antigravity, reads both views through the UI, and matches Accounts plus every Usage column against the relevant API. It re-hashes the accounts snapshot to prove nothing was mutated. It fails if either live wire has no account.

Mutation paths are proven by `scripts/prismctl-proof.sh` in the sandbox: `accounts select/auto-switch/distribute/affinity` (pool policy without needing an account), and `accounts pause/resume/priority/quota/remove` run against the isolated daemon when an account exists there. Never drive Remove during verification against the user's real accounts; a delete-path change is proven in the sandbox only. See management-views.md for the bridge drive recipe and the CAS gotchas.

## Gotchas

- A quota/rate-limit failure without a Retry-After header cools the account for the provider's `cooldownDefault` (live-verified against a real ChatGPT 429 `usage_limit_reached`): the account shows `cooling_down` with `cooldownUntil`, and further turns fail fast with `quota_exhausted "routing: pool exhausted"`.
- The quota `limit` is nullable, not zero-default: `no quota` (null) and `0` (`used / ?`) are different states. The proof asserts the exact string.
- `GET /api/v1/accounts/{id}/quota` backs the codex/antigravity live quota cells and the prismctl `accounts quota` command; other wires render the accounts-list snapshot.
- Policy writes are full provider replaces: the CLI and UI both send every observed pool field so absent fields are preserved. A partial write can silently drop pool settings.
- Quota values exist after inference has flowed or an active probe ran: on the live wires the quota endpoint probes on demand (5-minute TTL), so a fresh codex/antigravity account shows real numbers on first read. `quota unavailable` now means the probe could not answer — most commonly a missing credential blob (the daemon log names the account and reason); such an account needs a re-login before quota or inference can flow.
