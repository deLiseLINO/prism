# Auth flows (per-provider add-account)

The Auth view starts a real OAuth device/browser flow per provider, polls its state until terminal, and lists the provider's existing accounts below the card. The flow is the user's way to add a second account without touching the CLI.

## Sub-features

- Start login: `POST /api/v1/auth/{provider}/start` per provider card (codex, antigravity) — returns `{session, url}` and binds a loopback listener (codex on 127.0.0.1:1455, antigravity on 127.0.0.1:51121).
- Pending state: banner headline `Awaiting Codex approval` or `Awaiting Antigravity approval` with the hint `Complete the flow in your browser. Polling stops automatically on completion.`, an `Open Codex authorization page` / `Open Antigravity authorization page` button (`window.prism.shell.openExternal`), a `Check Codex status` / `Check Antigravity status` button, a `Cancel Codex login` / `Cancel Antigravity login` button, and a `Poll Codex` / `Poll Antigravity` toggle.
- Polling: `GET /api/v1/auth/{provider}/status?session=` every 1500ms until a terminal state (`complete`, `authorized`, `failed`).
- Terminal states: `Authorized`, `Authorization failed`, `Authorization cancelled` (renderer-local cancel, daemon still answers `pending` for the abandoned session until its 15-minute TTL expires), `Authorization session expired` (ApiError code `session_expired` or `unknown_session`). A session-less status answers `unauthorized` only when no account is registered for the provider.
- Cancel: renderer-local — clears the session and stops polling; there is no server-side cancel route. The pending session simply expires (15-minute TTL).
- Start failure: a failed start does not stay inside the dialog. `.error-toast` appears at the bottom right with title `Login failed to start`, the daemon error text, `Report anonymously` when `PRISM_REPORT_URL` is an https URL, and a `Close` button (`aria-label="Close"`). Escape also closes it. `loopback_unavailable` is the usual text when port 1455 or 51121 is already bound.
- Post-auth listing: existing accounts for the provider render under the card, filtered by provider `wire`; empty states are `No {provider} provider configured.` and `No accounts for {provider} yet.`.

## How to get to it (user POV)

Click `Accounts` in the left navigation (`#/usage`). Click `Add account`. The dialog offers `codex`, `antigravity`, and `cline`. Click `Start login`. A successful start shows the pending state. A failed start leaves the dialog open and shows the corner toast.

## Driving it with the harness

Run the safe proof:

```sh
verify/scripts/auth-proof.sh
```

It drives both cards from the real UI through CDP clicks and stops before any credential is entered: it asserts the exact pending banner and button set for Codex, clicks Cancel, asserts the return to `Not signed in`, repeats for Antigravity, and finally requires the daemon to still report zero accounts and no `authorized` session. It never opens the authorization URL in a browser, never enters credentials, and never touches the user's existing accounts — it runs against an isolated `HOME` and profile with a fresh sandbox config.

The CLI twin is inside `scripts/prismctl-proof.sh` and `scripts/api-sweep.sh`: `prismctl auth login codex --no-open` prints the URL and polls pending (the harness stops it), `prismctl auth status` still reports `codex: unauthorized`, no account appears in `accounts list --json`, and the HTTP surface returns `{session,url}` from `POST /api/v1/auth/codex/start` with `GET /api/v1/auth/codex/status?session=` answering `pending`. Same contract, both surfaces.

## Gotchas

- Never complete a login during verification: the proof's whole value is that it ends cancelled. A completed login would create a real account, which is out of bounds for the harness.
- Cancel is renderer-local state only. After cancel, the daemon still holds the pending session until it expires; polling it by session id still answers `pending`. That is not a leak in the UI, it is the TTL design.
- A busy callback port fails the start before any browser handoff. Hold `127.0.0.1:1455`, click `Start login` on `codex`, and the corner toast must contain `loopback_unavailable`. `Report anonymously` posts the short report to the bot and attaches the full log as a file. GitHub report buttons stay hidden while `GITHUB_REPORTS` is false.
- The `Check Codex status` / `Check Antigravity status` button is disabled on terminal states; asserting it clickable mid-flow proves the poll toggle wiring.
- The card filters accounts by providers whose `wire` matches the card (`codex`/`antigravity`); a provider id that differs from the wire still shows under the card. Do not match accounts by provider id string.
