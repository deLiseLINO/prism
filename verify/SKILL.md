---
name: verify-prism-desktop
description: Verify prism through its Electron renderer, registered daemon, management CLI, and generated client configs. Use after desktop, daemon, provider, account, integration, protocol, or streaming changes. Run isolated owned processes, save evidence, report unavailable coverage, and require real inference when the change affects it.
---

# Verify prism

Click the rendered control, observe its state, and inspect the resulting API response or file. A preload call is diagnostic evidence. It does not prove that a visible control works.

Read the [feature map](features/README.md) before selecting checks. A local pass, a live client response, a native tray action, and a continuous session are different claims.

## Launch

Run scripts from the repository root. The owned runtime builds no fixtures itself. The migrated Electron drivers build `./cmd/prism` and the desktop, then launch them with an isolated physical `HOME`, profile, and temporary directory. The CLI and API drivers build only the unified executable.

```sh
bash verify/scripts/desktop-controls.sh
PRISM_VERIFY_LIVE=0 bash verify/scripts/integration-drive.sh
```

Drivers using `owned-runtime.sh` reject pre-existing daemon and CDP listeners. Legacy smoke, vision, remote, and container scripts are not covered by this ownership guarantee. They never stop another instance to free a port. Set `PRISM_PORT` and `PRISM_CDP_PORT` to unused ports when another run is active. `agents-ui-drive.sh` uses `PRISM_AGENTS_UI_PORT` for its daemon port.

The runtime clears inherited client home overrides, management tokens, renderer URLs, and external configuration paths. Client configs use their normal paths inside the isolated home. A literal fixture shell supplies login-path discovery, so the user's startup files cannot expand the install fixture PATH.

Headless mode is the default. `PRISM_HEADLESS=0` shows the window but does not automate the native tray.

## Check prerequisites

Local drives need Go, Node.js, npm, Python 3.11 or later, `lsof`, and the installed Electron dependency. Structural integration checks also need the clients they actually invoke. Read the driver and preserve a missing-client result instead of replacing the binary with a stub.

Live state comes from `PRISM_VERIFY_STATE_DIR`, defaulting to `$HOME/.prism`. Live drivers stage `prism.json` and credentials privately. They reject symlinks and nonempty `accountsPath` settings before launching. They never change the source state.

Missing credentials or clients mean the relevant feature is unavailable. Record the attempted route and missing prerequisite. Do not turn that result into a pass. A provider failure after a request starts is a failed request, not unavailable coverage.

## Verify ownership and readiness

The runtime checks the chosen endpoint against `$HOME/.prism/daemon.json`, the health response, the exact sandbox binary, and the listener PID. Registration and health must agree on `id`, `pid`, and `version`.

For Electron, the CDP listener must belong to the captured app process. Exactly one page must match the built renderer URL. A page title or the first `type=page` entry is not ownership evidence.

After a crash or surprising result, recheck health and ownership before driving again. If a healthy renderer is stuck, relaunch the isolated app or restore a known view. Do not keep clicking blindly.

## Drive the desktop and integrations

```sh
bash verify/scripts/desktop-controls.sh
bash verify/scripts/agents-ui-drive.sh
bash verify/scripts/integration-drive.sh
PRISM_VERIFY_LEGACY=1 bash verify/scripts/integration-drive.sh
```

The desktop controls proof checks Overview state and endpoint, visible provider creation, model toggles, deletion, integration Apply and Rollback, and unknown-hash fallback.

The install UI proof enables the experimental actions and clicks Install on the codex row. Its package-manager and binary fixtures prove UI-to-job wiring, not an actual package installation.

The integration proof enables Other agents, applies each supported client config, reads the files, and invokes available real config loaders. It checks row and bulk actions, damaged-fence refusal, Auto-apply, and restoration after an owned daemon crash. Visiting Stats and Logs only proves that the views open with content.

The legacy mode seeds the earlier unfenced prism integration config and checks migration. Structural mode does not prove inference.

## Verify safe authentication

```sh
bash verify/scripts/auth-proof.sh
```

The proof opens Accounts, then Add account. It selects codex, starts login, waits for the pending badge and browser hint, and clicks Cancel login. It repeats for antigravity and requires zero accounts afterward.

Never open an authorization link or complete a login during this proof. Cancellation clears renderer state and stops polling. The daemon's abandoned pending session expires separately.

## Verify copied accounts and quota

```sh
PRISM_VERIFY_STATE_DIR=/path/to/private/state bash verify/scripts/quota-proof.sh
```

The proof reads copied accounts, per-account quota, and the Accounts UI. It compares the rendered values and confirms that non-quota account policy does not change. Raw account snapshots and screenshots remain private; the published result contains nonsecret comparison outcomes.

A missing account or credential is a coverage gap to report. It is not proof of the corresponding provider's quota behavior.

## Verify real client requests

```sh
PRISM_VERIFY_LIVE=1 bash verify/scripts/integration-drive.sh
bash verify/scripts/live-matrix.sh
```

Live integration runs one real request for client grok and one tool round trip for client omp using configs written by the UI. A final answer, a tool result, and displayed thinking are separate assertions.

The matrix is repeated-request stress across requested client and model pairs. Each invocation starts a fresh command. Aggregate elapsed time of 300 seconds does not prove one uninterrupted 300-second session. Preserve failed attempts and do not describe a retry-normalized result as zero transport failures.

A full compatibility claim needs real inference for every required client and model. Neither the structural driver nor the two-client stress run supplies that coverage. Never change user accounts or install global packages to fill a gap without authorization.

## Save evidence

Set `PRISM_VERIFY_EVIDENCE_DIR` to a new directory outside the sandbox when you need a stable destination. The quality gate treats that path as a run root and gives each driver a distinct child directory. The default is a unique `/tmp` directory. Record the printed path and verify its files after cleanup.

Keep the actual UI results, generated fixture configs, API comparisons, failed-request output, and screenshots that support the claim. The runtime writes binary identity, daemon identity, and a teardown receipt. Raw live state, credentials, provider infrastructure names, and unrestricted live logs are not public evidence.

Private live diagnostics remain in a mode-0700 directory after the run, including successful runs. The receipt names that private directory. Review and sanitize them before publication. A filename containing `sanitized` is not proof that its contents are safe.

## Clean up owned processes

The shared runtime owns cleanup. It stops Electron first, rechecks daemon identity, and runs isolated `prism service stop`. The app's normal quit only releases supervision; it does not stop the shared service.

Cleanup must confirm that the owned processes and registrations are gone and their ports are free. Evidence is copied and checked before sandbox removal. Raw live proof is retained separately with private permissions and its path recorded in the receipt. Finalization failures return nonzero, keep remaining private state, and never seal a successful completion receipt. Do not suppress that failure with `|| true`.

Never use global process-name matching, a port number, or a guessed child PID as permission to signal a process.

## Run the checks

```sh
bash verify/scripts/owned-runtime-proof.sh
node verify/scripts/cdp-ws-proof.mjs
bash verify/scripts/prismctl-proof.sh
bash verify/scripts/quality-gate.sh local
bash verify/scripts/quality-gate.sh live
```

The runtime proof exercises foreign-listener refusal, real registered service identity, repeated stop, durable evidence, and publication failure. The CDP proof rejects foreign and ambiguous renderers and foreign WebSocket endpoints.

The CLI proof exercises the unified `prism` binary. Empty argv enters the TUI. `prism service` without a subcommand is the missing-command usage error.

The local quality gate runs Go tests, race tests, vet, TypeScript checks, desktop tests, and the existing deterministic drivers. The live gate adds safe auth, copied quota, live integration, and repeated-request stress. Existing product-test failures remain failures; do not skip or weaken them to make verification green.

The remote drivers need separately authorized SSH prerequisites. They are not covered by a local pass. Native tray behavior and actual package-manager mutation likewise require their own real runs.
