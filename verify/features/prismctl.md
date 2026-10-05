# prismctl

The `prism` executable exposes the daemon management CLI, plus doctor, usage, stats, and agent reports. The verification script retains the name `prismctl-proof.sh`. It discovers the daemon via `PRISM_URL` (default `http://127.0.0.1:10200`). The only local input is a provider credential read from a file or stdin.

## Sub-features

- `status` — daemon health plus provider/account/generation summary.
- `doctor` — connectivity, CLI version, and config sanity (flags wire/endpoint mistakes).
- `auth login <codex|antigravity> [--no-open]` / `auth status [session]` — browser OAuth or provider/session state.
- `accounts list|pause|resume|priority|quota|remove|select` — the account family. `select <provider> <account>` pins a stored account for that provider. `auto` is a usage error.
- `providers list|add|edit|enable|disable|remove` — provider CRUD (`--wire`, `--endpoint`/`--base-url`, `--default-model`, `--model`). `add` and `edit` take the credential from `--credential-file PATH` or `--stdin`. Both together is a usage error. Empty stdin exits 1. The secret never enters argv and is never printed.
- `models list|enable|disable` — catalog and per-provider model toggles.
- `combos list|set|remove` — `--strategy failover|round_robin`, `--target provider/model[:weight]`, `--sticky-limit`, `--alias`.
- `routes list|set|remove` — alias -> provider/model or combo id.
- `integrations status|apply|rollback <codex|grok|omp>` — same actions as the UI.
- `usage` — quota usage across accounts.
- `stats [--range 1h|24h|7d|30d|all]` — request and token statistics across providers and models. Default range is 24h. A bad range exits 2.
- `agents [status] [codex|claude|grok|omp|pi|opencode|hermes]` — agent binary status. `agents install <agent> [--force]`, `agents update <agent>`, and `agents job <agent>` manage install jobs. An unknown agent or subcommand exits 2.
- Data-producing commands take `--json` for machine-stable output; `help` is plain text.
- Exit codes: 0 ok; 1 operational failure; 2 usage; 3 stale generation/version CAS (re-run hint); 4 integration refusal; 5 daemon unreachable; 6 malformed daemon response.

## How to get to it (user POV)

Run `prism <command> [subcommand] [flags]` with the daemon running. Use `--json` on data-producing commands. `prism help` prints the command tree. Empty arguments enter the TUI and can start the background service; they are not a usage-error probe.

## Driving it with the harness

```sh
bash verify/scripts/prismctl-proof.sh
```

`prismctl-proof.sh` builds the unified `./cmd/prism` executable and starts its registered service with an empty credential store. The default port is 18791. It checks these commands:

- `status`, `doctor`, `usage`, `help`, account, provider, model, combo, and route lists, agent status, integration status, and auth status exit 0. Provider and model JSON lists are decoded and checked. The script does not exercise `stats`.
- `auth login codex --no-open` prints the authorization URL, stays pending, is stopped by the harness, and no codex account ever appears — the cancelled-login contract, same as the UI auth proof;
- the full mutation ladder against the sandbox daemon: `providers add/edit/enable/disable/remove` (read back between each), `models enable/disable`, `combos set/remove`, `routes set/remove`, account actions only when a stored account exists. With the default empty store, the script records those actions as skipped;
- `integrations apply grok` + `integrations rollback grok` through the CLI, against an isolated HOME;
- usage errors exit 2 for bad provider, wire, strategy, or client, `accounts select` with `auto`, `agents install nonsense`, an unknown command, and `prism service` without a subcommand. A dead daemon exits 5.

Build the current entrypoint with `go build -o /tmp/prism-cli ./cmd/prism`. Run `PRISM_URL=http://127.0.0.1:18791 /tmp/prism-cli status --json` against your owned verification daemon.

## Gotchas

- The daemon base URL comes only from `PRISM_URL`. Any daemon not on 10200 (the harness on 18791, a verification daemon on 18787) needs `PRISM_URL` set or every command exits 5 (`daemon unreachable`).
- Exit 3 (stale CAS) is not an error to fix but a state to assert: the message tells the user to re-run, and the re-run succeeds because the fetch re-reads the generation.
- `accounts select`, `providers enable/disable`, and `models enable/disable` read then write every observed field so absent fields preserve. A hand-built minimal write body can clear pool settings. Mirror the CLI's own read-then-write pattern in any probe. Plain `providers add` and `edit` send only the flags given. `accounts select auto` does not clear a pin.
- `--endpoint` and `--base-url` are aliases; using both in one invocation is a usage error (exit 2).
- `accounts quota` is per-account; `usage` is the whole pool. They read different endpoints.
- `integrations apply` can refuse (exit 4) — against a user-edited managed block that is the correct outcome, matching the UI's alert.
