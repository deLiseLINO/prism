# Auth flows (per-provider add-account)

The Auth view starts a real OAuth device/browser flow per provider, polls its state until terminal, and lists the provider's existing accounts below the card. The flow is the user's way to add a second account without touching the CLI.

## Sub-features

- Start login: `POST /api/v1/auth/{provider}/start` per provider card (codex, antigravity) — returns `{session, url}` and binds a loopback listener (codex on 127.0.0.1:1455, antigravity on 127.0.0.1:51121).
- Pending state: banner headline `Awaiting Codex approval` or `Awaiting Antigravity approval` with the hint `Complete the flow in your browser. Polling stops automatically on completion.`, an `Open Codex authorization page` / `Open Antigravity authorization page` button (`window.prism.shell.openExternal`), a `Check Codex status` / `Check Antigravity status` button, a `Cancel Codex login` / `Cancel Antigravity login` button, and a `Poll Codex` / `Poll Antigravity` toggle.
- Polling: `GET /api/v1/auth/{provider}/status?session=` every 1500ms until a terminal state (`complete`, `authorized`, `failed`).
- Terminal states: `Authorized`, `Authorization failed`, `Authorization cancelled` (renderer-local cancel, daemon still answers `pending` for the abandoned session until its 15-minute TTL expires), `Authorization session expired` (ApiError code `session_expired` or `unknown_session`). A session-less status answers `unauthorized` only when no account is registered for the provider.
- Cancel: renderer-local — clears the session and stops polling; there is no server-side cancel route. The pending session simply expires (15-minute TTL).
- Start failure: `loopback_unavailable` renders a banner telling the user the callback port is busy.
- Post-auth listing: existing accounts for the provider render under the card, filtered by provider `wire`; empty states are `No {provider} provider configured.` and `No accounts for {provider} yet.`.

## How to get to it (user POV)

Click `Auth` in the left navigation (`#/auth`). One card each for Codex and Antigravity. Click `Start Codex login` or `Start Antigravity login`; the banner flips to `Awaiting Codex approval` or `Awaiting Antigravity approval`; click `Open Codex authorization page` or `Open Antigravity authorization page` to hand off to the system browser, or `Cancel Codex login` / `Cancel Antigravity login` to abandon. The card lists the provider's accounts below.

## Driving it with the harness

Run the safe proof:

```sh
scripts/verify/scripts/auth-proof.sh
```

It drives both cards from the real UI through CDP clicks and stops before any credential is entered: it asserts the exact pending banner and button set for Codex, clicks Cancel, asserts the return to `Not signed in`, repeats for Antigravity, and finally requires the daemon to still report zero accounts and no `authorized` session. It never opens the authorization URL in a browser, never enters credentials, and never touches the user's existing accounts — it runs against an isolated `HOME` and profile with a fresh sandbox config.

The CLI twin is inside `scripts/prismctl-proof.sh` and `scripts/api-sweep.sh`: `prismctl auth login codex --no-open` prints the URL and polls pending (the harness stops it), `prismctl auth status` still reports `codex: unauthorized`, no account appears in `accounts list --json`, and the HTTP surface returns `{session,url}` from `POST /api/v1/auth/codex/start` with `GET /api/v1/auth/codex/status?session=` answering `pending`. Same contract, both surfaces.

## Gotchas

- Never complete a login during verification: the proof's whole value is that it ends cancelled. A completed login would create a real account, which is out of bounds for the harness.
- Cancel is renderer-local state only. After cancel, the daemon still holds the pending session until it expires; polling it by session id still answers `pending`. That is not a leak in the UI, it is the TTL design.
- The loopback bind is per provider and per process: if port 1455 or 51121 is already bound, start fails with `loopback_unavailable` — in the user's environment that usually means another Prism login is in flight.
- The `Check Codex status` / `Check Antigravity status` button is disabled on terminal states; asserting it clickable mid-flow proves the poll toggle wiring.
- The card filters accounts by providers whose `wire` matches the card (`codex`/`antigravity`); a provider id that differs from the wire still shows under the card. Do not match accounts by provider id string.
