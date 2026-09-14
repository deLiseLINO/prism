# prismctl

`prismctl` is the CLI face of the daemon management API: every user action the desktop offers has a command here, plus doctor and usage reports. It discovers the daemon via `PRISM_URL` (default `http://127.0.0.1:8787`) and nothing else — no sockets, no files.

## Sub-features

- `status` — daemon health plus provider/account/generation summary.
- `doctor` — connectivity, CLI version, and config sanity (flags wire/endpoint mistakes).
- `auth login <codex|antigravity> [--no-open]` / `auth status [session]` — browser OAuth or provider/session state.
- `accounts list|pause|resume|priority|quota|remove|select|auto-switch|distribute|affinity` — the account family.
- `providers list|add|edit|enable|disable|remove` — provider CRUD (`--wire`, `--endpoint`/`--base-url`, `--default-model`, `--model`, `--credential-file`/`--stdin`).
- `models list|enable|disable` — catalog and per-provider model toggles.
- `combos list|set|remove` — `--strategy failover|round_robin`, `--target provider/model[:weight]`, `--sticky-limit`, `--alias`.
- `routes list|set|remove` — alias -> provider/model or combo id.
- `integrations status|apply|rollback <codex|grok|omp>` — same actions as the UI.
- `usage` — quota usage across accounts.
- Data-producing commands take `--json` for machine-stable output; `help` is plain text.
- Exit codes: 0 ok; 1 operational failure; 2 usage; 3 stale generation/version CAS (re-run hint); 4 integration refusal; 5 daemon unreachable; 6 malformed daemon response.

## How to get to it (user POV)

`prismctl <command> [subcommand] [flags]` from any directory, with the daemon running. Use `--json` on data-producing commands in scripts. `prismctl help` prints the whole tree.

## Driving it with the harness

```sh
bash verify/scripts/prismctl-proof.sh
```

`prismctl-proof.sh` is the executable proof for the whole CLI surface. It builds prismd and prismctl, boots an isolated daemon (port 18791, empty credential store, throwaway config), and asserts all of this in one run:

- every read command exits 0 (`status`, `doctor`, `usage`, `help`, `accounts/providers/models/combos/routes list`, `integrations status [client]`, `auth status`), with `--json` variants decoding as JSON and naming catalog models;
- `auth login codex --no-open` prints the authorization URL, stays pending, is stopped by the harness, and no codex account ever appears — the cancelled-login contract, same as the UI auth proof;
- the full mutation ladder against the sandbox daemon: `providers add/edit/enable/disable/remove` (read back between each), `models enable/disable`, `combos set/remove`, `routes set/remove`, `accounts select/auto-switch/distribute/affinity` (pool policy without needing an account);
- `integrations apply grok` + `integrations rollback grok` through the CLI, against an isolated HOME;
- usage errors exit 2 (bad provider, bad wire, bad strategy, bad client, unknown command, no command) and a dead daemon exits 5.

Manual probe when iterating: `go build -o /tmp/prismctl ./cmd/prismctl && PRISM_URL=http://127.0.0.1:18791 /tmp/prismctl status --json`.

## Gotchas

- The daemon base URL comes only from `PRISM_URL`: a verification daemon on 18787 needs `PRISM_URL` set or every command exits 5 (`daemon unreachable`).
- Exit 3 (stale CAS) is not an error to fix but a state to assert: the message tells the user to re-run, and the re-run succeeds because the fetch re-reads the generation.
- `accounts select/auto-switch/distribute/affinity`, `providers enable/disable`, and `models enable/disable` read then write every observed field so absent fields preserve — a hand-built minimal write body can silently clear pool settings. Mirror the CLI's own read-then-write pattern in any probe. Plain `providers add`/`edit` send only the flags given.
- `--endpoint` and `--base-url` are aliases; using both in one invocation is a usage error (exit 2).
- `accounts quota` is per-account; `usage` is the whole pool. They read different endpoints.
- `integrations apply` can refuse (exit 4) — against a user-edited managed block that is the correct outcome, matching the UI's alert.
