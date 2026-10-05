# Client install and update

prism installs and updates seven integration clients. The clients are codex, claude, grok, omp, pi, opencode, and hermes. The management routes and `prismctl agents` commands start asynchronous jobs. Installation status comes from the daemon's PATH, not persisted install state. Status reads files and local prerequisites without running subprocesses.

## Management contract

- `GET /api/v1/agents` lists the integrations in their existing order. Status contains `installed`, `source`, `path`, `canUpdate`, `reason`, and the last `job`.
- `GET /api/v1/agents/{id}` returns one status. An unknown id returns 404 `not_found`.
- `POST /api/v1/agents/{id}/install` starts a fresh install when the client is absent. When the client is present, it repairs the selected installation through its proven owner. Reinstall never chooses another package manager because that manager ranks higher for fresh installs.
- `?force=true` adds `--force` to npm and Bun install commands. Homebrew repairs use `reinstall` with the observed formula or cask. Script repairs use a validated destination and installer.
- `POST /api/v1/agents/{id}/update` updates the selected installation. A missing client returns 400 `not_installed`.
- A started job returns 202 with the existing job envelope. An unproven owner or missing prerequisite returns 200 with `state: unsupported` and a reason.
- Concurrent jobs for the same binary or global package root return 409 `install_active`. Independent roots can run concurrently. Shutdown closes admission and interrupts active jobs.
- `GET /api/v1/agents/{id}/job` returns the job envelope. A client with no previous job has state `idle`.

## Installation ownership

`source` describes the selected path. It does not authorize maintenance. An installed client can have `source: npm` and `canUpdate: false`.

npm ownership requires a global `<prefix>/lib/node_modules/<package>` layout, a matching package manifest and bin entry, and a selected `<prefix>/bin/<client>` link. A project-local package, nested package tree, unrelated package, or opaque wrapper refuses maintenance. Update and reinstall pass `--prefix` with the observed prefix and preserve the observed package alias. The client opencode retains an existing `opencode-ai` installation. Fresh npm installs use `@opencode/cli`.

Homebrew ownership requires a recognized versioned `Cellar` or `Caskroom` path and a selected entry under the same prefix. Local capability requires an executable under the selected prefix. `canUpdate` permits attempting maintenance, not proof of a live executable response. Before mutation, the selected executable must return that prefix on stdout from `brew --prefix`. Maintenance uses the observed formula or cask token.

Bun ownership requires a selected bin linked to a matching global package manifest. Each mutation pins `BUN_INSTALL_GLOBAL_DIR` and `BUN_INSTALL_BIN` in its own environment. The base directory is `BUN_INSTALL` when set, then `XDG_CACHE_HOME/.bun`, then `HOME/.bun`. `BUN_INSTALL_GLOBAL_DIR` and `BUN_INSTALL_BIN` independently override the global root and bin defaults. An unproven root refuses maintenance. Fresh installation of the client omp uses its official scoped package.

pnpm installations remain visible but require manual maintenance because this implementation does not prove their global destination.

Recognized native entries use the selected absolute client's updater. Arbitrary files cannot gain update authority merely because the integration defines a self-update command. Native maintenance for the client opencode refuses because the installer generation is unproven. The client hermes uses its official downloaded installer with `--non-interactive`, and its recognized native updater receives `update --yes`. Reinstall requires manual maintenance because a launcher does not prove the original source directory. No npm installer is offered for that client.

## Execution and verification

Executable lookup uses the Manager's environment. Mutation commands run through absolute executables, and shebang runtimes inherit that same environment. A fresh npm install reads the selected npm's `prefix -g` stdout and binds verification to that destination. Prefix probes collect bounded stdout separately from stderr warnings. Before fetching a script or running a mutation, each fresh destination must have a canonical parent directory on the unchanged Manager PATH. A PATH-listed parent can be absent until installation. Invisible destinations refuse without writing a client, or fall through to another declared plan. Existing off-PATH entries remain protected.

After mutation, verification resolves the client under the unchanged Manager environment. The selected entry and its stable owner, root, and package must match the job's target. Versioned native and Homebrew files can change without changing the stable owner. An unrelated copy cannot satisfy verification.

The absolute selected client must exit successfully and print a version token containing digits separated by a period. Empty output and output without a version fail. An unchanged version can succeed. No manifest-to-CLI version equality is required.

Each version probe has a 5-second timeout. Only a probe that reaches its own deadline receives one 60-second retry. The full 15-minute job deadline also covers verification. Combined mutation and version output is bounded during collection and retains its last 4096 bytes. On Unix, each command owns a private process group. Cancellation kills the group, and cleanup kills remaining descendants before the command returns, even after the leader exits. Pipe-drain timeout errors remain failures. Children that deliberately leave the group are outside this cleanup policy. Script downloads require absolute HTTPS, permit only HTTPS redirects, cap at 4 MiB, and are removed after execution.

## UI and CLI

The existing experimental `agentActions` flag controls the desktop Install, Reinstall, and Update buttons. The daemon routes and CLI remain available regardless of that UI flag.

`prismctl agents status [client] [--json]`, `install <client> [--force]`, `update <client>`, and `job <client>` use the same routes. Refusal reasons appear in status and the unsupported job.

## Verification scripts

`verify/scripts/agents-drive.sh` builds an isolated daemon and exercises the API install, update, status, job, and refusal routes. Its fake npm writes a real global package layout with a manifest and symlink. Its native fixture answers a version probe. HOME, account names, shell, and PATH are isolated, including login-shell PATH discovery.

`verify/scripts/agents-ui-drive.sh` exercises Install, Update, and Reinstall through the desktop UI and saves the jobs, statuses, and screenshot. Its npm, shell, HOME, and PATH fixtures remain in the temporary directory. Both scripts refuse to start if their target daemon port already serves a health endpoint.

These scripts do not install or update genuine clients. Older container-matrix results do not verify the current ownership contract.
