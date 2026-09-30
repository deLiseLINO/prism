# Auth flows (Add account modal)

The Accounts view opens an `Add account` modal. It starts a real OAuth browser flow for one provider, polls its state until terminal, and reports the result inside the dialog. It is the way to add a second account without touching the CLI.

## Sub-features

- Provider picker. A `Provider` segmented group (`role=group`, `aria-label="Login provider"`) with `.msm-seg-btn` buttons `codex`, `antigravity`, `cline`. `codex` is selected by default. The buttons are disabled while a session exists.
- Start login. `POST /api/v1/auth/{provider}/start` returns `{session, url}` (plus `userCode` when the provider uses a device code) and binds a loopback listener. Codex binds 127.0.0.1:1455 (`/auth/callback`). Antigravity binds 127.0.0.1:51121 (`/callback`).
- Header. A provider badge and, once a session exists, a state badge (`pending`, `complete`, `authorized`, `failed`, `unauthorized`).
- Pending state. The body shows `Complete the login in your browser. This window checks automatically until it finishes.` When the provider returns a user code it shows `Enter code <code> in the browser, then wait here.` instead. The footer holds `Cancel login`, `Copy link` (turns into `Copied` for 1.5s after writing the URL to the clipboard), and `Open in browser` (`window.prism.shell.openExternal`).
- Polling. `GET /api/v1/auth/{provider}/status?session=` every 1500ms until a terminal state (`complete`, `authorized`, `failed`, `unauthorized`). There are no Check or Poll buttons.
- Success. Banner `Authorized` with `The account joined the pool.` The footer shows `Close` and `Start another login`. `onAdded` refreshes usage and providers once, so the new account shows in the Accounts view.
- Failure. Banner `Authorization failed` with `Start a new login to retry.` It appears for `failed`, `unauthorized`, an expired session (ApiError code `session_expired` or `unknown_session`), and any other poll error. Terminal failures offer `Close` and `Start another login`.
- Cancel. `Cancel login` is renderer-local. It clears the session and stops polling, and the dialog returns to the idle state with `Close` and `Start login`. There is no server-side cancel route. The daemon keeps the pending session until its 15-minute TTL expires and still answers `pending` for it.
- Start failure. The dialog stays open in the idle state. An `.error-toast` (`role=alert`, portaled to body) appears at the bottom right with title `Login failed to start`, the daemon error text, `Report` when `PRISM_REPORT_URL` is an https URL, and a `Close` button (`aria-label="Close"`). Escape closes the toast. `loopback_unavailable` (HTTP 503) is the usual text when port 1455 or 51121 is already bound.
- Dismiss. The header `Close` icon button (`aria-label="Close"`), the footer `Close`, a click on the veil, and Escape all close the modal.
- Daemon status semantics. A session-less status answers `authorized` when an account is registered for the provider and `unauthorized` otherwise. An unknown or expired session answers 400 with `unknown_session` or `session_expired`. A daemon built without an auth service answers 501 `unsupported`.

## How to get to it (user POV)

Click `Accounts` in the left navigation (`#/usage`). The page heading is `Accounts`. Click `Add account` in the header actions. The dialog opens with `codex` selected. Pick a provider and click `Start login`. A successful start shows the `pending` badge and the browser hint. A failed start leaves the dialog idle and shows the corner toast.

## Driving it with the harness

Run the safe proof:

```sh
verify/scripts/auth-proof.sh
```

It builds prismd and the desktop app, launches Electron with an isolated `HOME` and profile, and drives the real UI through CDP. Selectors it uses are `nav button` containing `Accounts`, `main h1` equal to `Accounts`, `main .head-actions button` equal to `Add account`, `[role=dialog][aria-label="Add account"]`, `.msm-seg-btn` by text, and `footer button` by text. For Codex it clicks the `codex` segment, clicks `Start login`, waits for `Complete the login in your browser` and the `pending` badge, and requires `Open in browser`, `Copy link`, and `Cancel login`. It screenshots, clicks `Cancel login`, and requires the footer to return to `Start login`. It repeats the start, pending, and cancel steps for `antigravity`, checks the daemon sessions in between, and finally requires the daemon to report zero accounts and no `authorized` session. It never opens the authorization URL, never enters credentials, and never touches the user's existing accounts. It refuses to run if a daemon already answers on `PRISM_PORT` (default 18787).

The CLI twin is inside `scripts/prismctl-proof.sh` and `scripts/api-sweep.sh`. `prismctl auth login codex --no-open` prints the URL and polls pending (the harness stops it). `prismctl auth status` still reports `codex: unauthorized`, no account appears in `accounts list --json`, and the HTTP surface returns `{session,url}` from `POST /api/v1/auth/codex/start` with `GET /api/v1/auth/codex/status?session=` answering `pending`. Same contract, both surfaces.

## Gotchas

- Never complete a login during verification. The proof's value is that it ends cancelled. A completed login would create a real account, which is out of bounds for the harness.
- Cancel is renderer-local. After cancel, polling the abandoned session id against the daemon still answers `pending` until the TTL expires. That is the design, not a leak.
- The provider segment is disabled while a session is live, so switching provider needs `Cancel login` first. The proof relies on this ordering.
- A busy callback port fails the start before any browser handoff. Hold `127.0.0.1:1455`, click `Start login` on `codex`, and the corner toast must contain `loopback_unavailable`. `Report` posts the short report to the bot and attaches the full log as a file. GitHub report buttons stay hidden while `GITHUB_REPORTS` is false.
- The modal is portaled to `document.body`, so query it from `document`, not from inside `main`.
- The state badge text is lowercase (`pending`). The proof matches it case-sensitively.
