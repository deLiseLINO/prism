---
name: verify-prism-desktop
description: Drive the real Prism Electron app, its prismd daemon, prismctl, every management route, and the Grok, OMP, Codex, Claude, Pi, opencode v1, opencode2, and Hermes clients. Use after desktop, integration-writer, protocol, streaming, reasoning, daemon lifecycle, provider, CLI, or account changes. Proof requires actual UI clicks, generated-config parsing by each client, read-only quota reads, safe (cancelled) auth flows, live inference matrices for Luna, Gemini 3.7 Flash, and GLM 5.3 with >=300-second sessions, saved evidence, and isolated client homes.
---

# Verify Prism desktop

Prism is an Electron control plane for `prismd`. Verification must cover the same boundaries a user crosses: click the renderer control, observe the renderer state, read the resulting file, load that file with the real client, read real account quota without mutating it, drive an auth flow to its pending state and back without ever completing a login, and run real inference when the change touches it.

A preload call is diagnostic evidence only. It cannot prove that a button is enabled, wired to the expected action, refreshes its state, or displays failures.

## Launch

Run from the repository root. The helpers build the current `prismd` and desktop bundles, create an isolated `HOME`, start Electron on port 18787 with CDP on 19222, and seed the default `~/.codex`, `~/.grok`, `~/.omp`, `~/.claude`, `~/.pi`, `~/.config/opencode`, and `~/.hermes` paths. They do not set client-specific home overrides, because those can hide a default-path bug. Every helper runs Electron headless by default (`PRISM_HEADLESS=1`): no window shows, no tray icon initializes, and CDP drives the hidden renderer. Set `PRISM_HEADLESS=0` only when a human needs to watch the run. The daemon port comes from `PRISM_PORT` everywhere (the isolated proofs fall back to `PRISMCTL_PROOF_PORT`, defaults 18791/18792/18793, and `PRISM_SMOKE_PORT`, default 18789); CDP comes from `PRISM_CDP_PORT`.

```sh
verify/scripts/integration-drive.sh
```

This structural mode clicks Apply for Codex, Grok, OMP, Claude, Pi, opencode v1, opencode2, and Hermes, checks the managed state in the rendered UI, reads the generated files, and makes the installed Grok, OMP, Pi, opencode v1, Claude, and Hermes binaries parse them. opencode2 beta-19086 has no headless parse probe (its `models` command is a silent no-op in an isolated home); the structural run asserts its file contract byte-level instead and live mode covers real use. It does not prove inference.

Run the legacy migration mode when Apply behavior changes. It seeds the unfenced Prism Grok tables written by the previous setup and requires the UI action to migrate them:

```sh
PRISM_VERIFY_LEGACY=1 \
  verify/scripts/integration-drive.sh
```

For any change involving providers, streaming, Responses/Chat/Messages conversion, reasoning, client compatibility, or generated model entries, live mode is required:

```sh
PRISM_VERIFY_LIVE=1 \
  verify/scripts/integration-drive.sh
```

Live mode copies `~/.prism/prism.json` and `~/.prism/credentials` into the temporary sandbox. The sandbox daemon may refresh only its private copy. Cleanup deletes it. Credentials never enter evidence. Override the source with `PRISM_VERIFY_STATE_DIR` and models with `PRISM_VERIFY_GROK_MODEL` or `PRISM_VERIFY_OMP_MODEL`.

Readiness requires both checks:

```sh
curl -sf http://127.0.0.1:18787/api/v1/health
curl -sf http://127.0.0.1:19222/json/version
```

## Doctor

Run these before driving or after any surprising result:

```sh
test -x "$(command -v grok)"
test -x "$(command -v omp)"
test -x "$(command -v codex)"
test -x node_modules/.bin/electron
test -x "$(command -v perl)"
test -x "$(command -v claude)"
test -x "$(command -v pi)"
test -x "$(command -v opencode)"
test -x "$(command -v opencode2)"
test -x "$(command -v hermes)"
test -f ~/.prism/prism.json
test -d ~/.prism/credentials
curl -sf http://127.0.0.1:18787/api/v1/health
curl -sf http://127.0.0.1:19222/json/version
curl -sf http://127.0.0.1:18787/api/v1/agents | grep -q '"id":"codex"'
```
Every check except the last three is a prerequisite. The last three should fail before launch and answer after launch. The agents check also proves the new management family is served by the running binary. A live verification without Prism credentials is `VERIFIED_UNREACHABLE`, not a pass. A daemon already listening on the verification port (default 18787) means the user's own instance is running: the gate refuses to double-drive it rather than killing a shared process.

## Drive

Drive user-facing behavior through rendered controls:

1. Click the `Integrations` navigation button.
2. Find the card by its visible heading, such as `grok`.
3. Assert its Apply button exists and is enabled.
4. Click Apply.
5. Fail on any rendered alert.
6. Wait until the same card renders the exact `managed` badge.
7. Read the generated file from the isolated client home.
8. Run the real client against that file.

Use `window.prism.*` only to diagnose a failed UI path. A bridge call that succeeds while the button path fails proves a renderer regression.

### Auth flows (safe proof)

The Accounts view's `Add account` dialog (`#/usage`, labeled Accounts in the nav) has one card each for Codex and Antigravity. `scripts/auth-proof.sh` drives both add-account flows from the real UI and stops before any credential is entered:

1. Seed an isolated sandbox daemon config with `codex` and `antigravity` providers.
2. Click `Accounts`, click `Add account`, then `Start Codex login` on the Codex card, and assert the banner reaches exactly `Awaiting Codex approval` with the hint `Complete the flow in your browser. Polling stops automatically on completion.`, plus the `Open Codex authorization page`, `Check Codex status`, and `Cancel Codex login` buttons and the `Poll Codex` toggle. The Antigravity card follows the same pattern with its own name in every label.
3. Click `Cancel` and assert the card returns to `Not signed in`.
4. Repeat start, pending assertion, and cancel for the Antigravity card.
5. Assert the daemon still reports zero accounts and no `authorized` session: the proof never completed a login.

The proof never opens the authorization URL in a browser, never enters credentials, and never touches the user's existing accounts. It runs against an isolated `HOME` and an isolated profile. The cancelled pending session simply expires server-side (15-minute TTL); there is no server-side cancel route, and the Cancel button is renderer-local state.

### Accounts and quota (read-only proof)

`scripts/quota-proof.sh` copies the real `~/.prism` state into the sandbox daemon, then reads quota through both surfaces without any write:

1. Snapshot `GET /api/v1/accounts` before anything else and hash it.
2. Open `#/usage` (labeled Accounts in the nav) through the nav click, capture every rendered row (account, state, priority, quota cell, cooldown), and screenshot.
3. For every account: match the UI quota cell against the API value. Codex and antigravity rows use the live per-account quota endpoint: `source: unknown` renders `quota unavailable`, a ready answer renders `used / limit` or `used / ?`. Rows for other wires render exactly `no quota` on a null limit. Match the Usage view's `used` figure and `source` against `GET /api/v1/usage`.
4. Fetch `GET /api/v1/accounts/{id}/quota` for each account (the prismctl backing endpoint). The endpoint actively probes the provider when its stored snapshot is stale, so codex and antigravity rows show real numbers on first read.
5. Re-snapshot accounts and fail if any account field except `quota` changed: quota reads refresh the stored quota snapshot by design; pause, resume, priority, and remove are never invoked.

The proof requires accounts for both `codex` and `antigravity` providers to exist; it fails (not unreachable) if only one is present, because missing accounts are a coverage gap, not a credentials gap. A missing credential blob for an account is a real finding, not a proof failure: the quota cell renders `quota unavailable` and the daemon log names the account and reason.

### Live matrix (Grok and OMP x Luna, Gemini 3.7 Flash, GLM 5.3)

`scripts/live-matrix.sh` runs sequential real prompts per supported client/model pair until the pair has been alive for at least 300 seconds, then one final short marker request; a pair is a >=300-second session. The checked application models are the three models a user selects inside the clients: Luna (`codex/gpt-5.6-luna`), Gemini 3.7 Flash (`antigravity/gemini-3.7-flash`), and GLM 5.3 (`router/glm-5.3`). These are not subagent pool lanes.

The matrix derives selectors from what Apply wrote: grok uses the `prism-` aliases from `~/.grok/config.toml` (`prism-codex-gpt-5-6-luna`, `prism-antigravity-gemini-3-7-flash`, `prism-router-glm-5-3`); OMP uses the prism provider leaf paths (`prism/codex/gpt-5.6-luna`, `prism/antigravity/gemini-3.7-flash`, `prism/router/glm-5.3`). Each pair issues sequential long-generation prompts (essays for codex/antigravity, counting runs for the router provider, reasoning effort off for router pairs) under `--output-format streaming-json` for grok and `--mode json --print` for OMP, each bounded by a ceiling timeout so a hung generation fails instead of blocking. Once the 300-second floor is reached, the pair sends one final short request whose response must end with the unique marker. Failed attempts are retried up to three times and preserved under `pairs/<pair>-failed/`; the pair transcript contains only completed requests.

Each pair must satisfy every assertion or the matrix fails:

- elapsed >= 300 seconds (`PRISM_MATRIX_MIN_SECONDS`, ceiling `PRISM_MATRIX_CEILING_SECONDS` default 540);
- client exit code 0;
- a substantive final response: the marker appears exactly once in the final assistant message and total assistant characters across the pair are at least 1000;
- zero transport errors: no `error`/`agent_error` event, no `stopReason: "error"`, no `errorMessage`, no failed tool execution, no `upstream_transport` envelope in the stream; corroborated by `GET /api/v1/usage` snapshots before and after the run showing no account slipped into `cooling_down` or `soft_avoid`;
- daemon health 200 before and after each pair.

The runner records model ID, client, provider, the account the pool leased, start and end timestamps, elapsed seconds, exit code, transport error count, marker count, final response length, stdout, stderr, and the daemon log slice for that pair, then writes `run.json` and a `manifest.sha256` over every evidence file. `scripts/assert-matrix-run.mjs` re-verifies the whole run: matrix completeness (all six pairs), every per-pair assertion, and every checksum.

Use the user's existing Codex and Antigravity accounts only for these inference tests. Do not sign in, refresh credentials, edit accounts, or expose secrets during a matrix run.

## Evidence

Each helper prints a permanent evidence directory. Structural and live integration runs default to `/tmp/prism-verify-evidence.<timestamp>.<pid>`; auth-proof, quota-proof, and live-matrix runs default to `./verify/evidence/<kind>/<runId>/` inside the repository so they survive reboot. Override with `PRISM_VERIFY_EVIDENCE_DIR`. Keep:
- `apply-codex.json`, `apply-grok.json`, `apply-omp.json`, `apply-claude.json`, `apply-pi.json`, `apply-opencode.json`, `apply-opencode2.json`, and `apply-hermes.json`;
- on UI failure, `integrations-failure.json`, `integrations-failure.png`, and `apply-<client>-diagnostic.json`;
- `integrations.png`;
- `codex-config.toml`, `grok-config.toml`, `omp-models.yml`, `claude-settings.json`, `pi-models.json`, `opencode-config.json` (shared by v1 and v2), and `hermes-config.yaml`;
- `codex-login-status.txt`, `grok-models.txt`, `omp-models.txt`, `claude-doctor.txt`, `pi-models.txt`, `opencode-models.txt`, `hermes-providers.txt`, `hermes-config-check.txt`, and `opencode2-models.txt` (a recorded SKIPPED marker when the headless probe is unavailable);
- in live mode, `grok-live.txt`, `omp-live.ndjson`, `omp-live.stderr`, `omp-assertion.json`, and `omp-assertion.stderr`;
- `app.log` and `build.log`;
- in auth-proof runs, `auth-codex-pending.json`, `auth-codex-cancelled.json`, `auth-antigravity-pending.json`, `auth-antigravity-cancelled.json`, the matching screenshots, and the pre/post account snapshots;
- in quota-proof runs, `accounts.json`, `accounts-after.json`, `providers.json`, `accounts-ui-rows.json`, `usage.json`, `usage-ui-rows.json`, `quota-match.json`, URL-encoded `quota-<account>.json` files for live wires, and the Accounts/Usage screenshots;
- in live-matrix runs, the per-pair directories under `pairs/` (command line, stdout or NDJSON, stderr, daemon log slice, `result.json`), `daemon/health.json`, `daemon/prism.json.sanitized`, `daemon/usage-before.json`, `daemon/usage-after.json`, `run.json`, `manifest.sha256`, and `assertion.json`.

Proof must name the exact files inspected. Do not summarize a failed live run as a structural pass. If the UI click fails, preserve its evidence and stop. Do not replace it with a direct bridge call.

## Cleanup

Each helper owns its Electron PID, its unique daemon port, and its temporary home. Before launching, it refuses with exit 1 if a daemon already answers on its verification port, so a shared user instance is never double-driven. Its exit trap:

1. sends SIGTERM to the Electron PID it started;
2. terminates only the daemon it started itself — the PID recorded from its own Electron supervisor or its own spawn — and never a prismd it did not launch;
3. copies non-secret evidence out of the sandbox;
4. deletes the sandbox, including copied credentials.

After cleanup, health on port 18787 must fail and the printed evidence directory must still exist. Evidence directories under the skill's `evidence/` tree are never deleted by cleanup; prune them manually when they are no longer needed.

## Helpers

`scripts/quality-gate.sh local` runs the deterministic local coverage: `go test ./...`, `go test -race ./...`, `go vet ./...`, `npm run typecheck`, the desktop vitest suite, `prismd-smoke.sh`, `prismctl-proof.sh`, `desktop-controls.sh`, `api-sweep.sh`, and structural `integration-drive.sh`. It prints `VERIFIED local`.

`scripts/quality-gate.sh live` runs everything in local mode first, then the live sequence: `auth-proof.sh`, `quota-proof.sh`, live `integration-drive.sh`, and `live-matrix.sh`. It prints `VERIFIED live`. Missing credentials, missing clients, or a missing Electron build return `VERIFIED_UNREACHABLE` with exit 3 — never a false pass. An unknown mode returns exit 2. A daemon already answering on the verification port (`PRISM_PORT`, default 18787) fails with exit 1 in both modes, with a message telling the operator to stop it, because the skill refuses to double-drive a shared instance.

`npm run verify` and `npm run verify:live` (repository root, and the same names under `apps/desktop`) are the literal npm invocations of the two modes.

`scripts/integration-drive.sh` is the structural and live integration proof. Structural mode verifies the UI and config loaders, per-card Apply and Rollback updating cards in place, and the strip-level Apply all and Rollback all buttons converging every card without remounts or entry animations. `PRISM_VERIFY_LEGACY=1` verifies upgrade behavior from the previous unfenced Grok configuration. `PRISM_VERIFY_LIVE=1` additionally verifies real Grok inference and real OMP inference with a read-tool round trip and visible thinking.

`scripts/auth-proof.sh` proves the Codex and Antigravity add-account flows from the real UI: start, pending device-authorization state, cancellation, and zero side effects, never completing a login.

`scripts/quota-proof.sh` proves read-only quota visibility for existing Codex and Antigravity accounts through both the Accounts and Usage views, matched against the management API, with a before/after hash proving no mutation.

`scripts/live-matrix.sh` runs the Grok and OMP live matrix across Luna, Gemini 3.7 Flash, and GLM 5.3 with one >=300-second session per supported pair. Override the pair lists with `PRISM_MATRIX_GROK_MODELS` and `PRISM_MATRIX_OMP_MODELS`, the floor with `PRISM_MATRIX_MIN_SECONDS`, and the ceiling with `PRISM_MATRIX_CEILING_SECONDS`.

`scripts/prismctl-proof.sh` proves the whole prismctl CLI surface against an isolated daemon: every read command with `--json` decoding, the provider/models/combos/routes/accounts mutation ladder, `auth login --no-open` staying pending and cancelled without creating an account, integrations apply/rollback, usage errors exiting 2, and a dead daemon exiting 5.

`scripts/desktop-controls.sh` proves the desktop control surface through the real UI: the Overview daemon card rendering ready state and its endpoint (matched against the health endpoint), provider create/model-toggle/delete, integrations Apply then Rollback rendering the `unmanaged` badge again, the unknown-hash fallback to the Overview view, and daemon teardown when Electron quits.

`scripts/remote-switch-proof.sh` proves host switching through the real Machines UI: Manage on a `self` host (loopback SSH with its own daemon port) flips every view to that daemon, Providers shows its models and not the local ones, and Back to this machine restores the local daemon. It needs key-auth loopback `ssh localhost` and a seeded external host; it enables the experimental machines flag first.
`scripts/remote-install-proof.sh` proves installing prismd on a remote machine from the real Machines UI behind the experimental flag: the Install prismd button stays hidden while the flag is off, the Experimental tab toggles it on, the button then streams the bundled binary over ssh into `~/.prism/remote`, starts it on `PRISM_REMOTE_DAEMON_PORT` (default 18802), flips the host to external with that port, and Manage routes views through the freshly installed daemon. It needs key-auth loopback `ssh localhost` and leaves no `~/.prism/remote` behind.

Every script takes `PRISM_PORT` and `PRISM_CDP_PORT`. When another worktree runs its own verification at the same time, both defaults can collide: a daemon-port collision fails the launch gate honestly, but a CDP-port collision is worse (the driver can attach to the *other* worktree's window and click its UI). Always pick free ports for both when anything prism-shaped is running: scan upward from 18810/19240, or export `PRISM_PORT` and `PRISM_CDP_PORT` explicitly.

`scripts/api-sweep.sh` proves the whole management and inference HTTP surface against an isolated daemon: every management GET, the negative surface (404/405/400/malformed JSON/stale-CAS 409), provider/combo/route mutation ladders with read-back, auth start `{session,url}` and pending status, and the inference shapes (`/v1/models`, `count_tokens` through a `claude-` alias, no-route 404s, wrong method 405).

`scripts/assert-omp-output.mjs` parses OMP NDJSON and rejects missing final output, missing thinking, missing tool calls/results, malformed JSON, and error events.

`scripts/cdp-eval.mjs` evaluates a renderer expression or statement sequence through Electron CDP; an optional third argument raises the timeout in milliseconds. Use it for UI automation and diagnostics.

`scripts/cdp-ws.mjs` picks the page target's WebSocket URL from a CDP port, so evaluation never lands on a non-page target.

`scripts/cdp-screenshot.mjs` captures the current Electron page into the evidence directory.

`scripts/prismd-smoke.sh` checks daemon startup, health, graceful shutdown, and credential-store creation without Electron. It is necessary for daemon work but cannot prove a desktop or client integration.
