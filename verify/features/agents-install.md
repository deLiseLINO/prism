# Agent install and update

Prism installs and updates the agent binaries behind its integrations: codex, claude, grok, omp, pi, opencode, and hermes. The contract is the five `/api/v1/agents*` management routes plus the `prismctl agents` family. Install state is derived from PATH, never stored: a restart re-derives the truth. One job per binary runs at a time.

## Sub-features

- `GET /api/v1/agents` lists all eight ids in integrations order with `installed`, `source`, `path`, `canUpdate`, `reason`, and the last `job`.
- `GET /api/v1/agents/{id}` returns one status; unknown id is 404 `not_found`.
- `POST /api/v1/agents/{id}/install` starts a job: 202 with the job envelope, `?force=true` appends the method's force argument (npm `--force`, brew `--force`), a plan-less environment returns 200 with state `unsupported`, and a running job for the same binary returns 409 `install_active`.
- Script plans (codex, claude, grok, opencode, omp) download the official installer to a temp file inside the job and exec `[interpreter, tempPath]`; the job's `command` field keeps the readable `bash <url>` form. Only absolute HTTPS URLs are fetched, redirects must stay HTTPS, downloads cap at 4 MiB, and the temp file is removed when the job ends.
- `POST /api/v1/agents/{id}/update` derives the update command from the detected install source: npm/pnpm/bun refresh the package, brew upgrades the formula or cask, script sources run the binary's self-update (`codex update`, `claude update`, `grok update`, `omp update`, `pi update`, `hermes update --yes`), opencode (no self-update) reruns npm or the fetched install script. A source whose manager the agent has no plan for refuses by name (`state: unsupported`, e.g. "omp has no npm package"), never an empty package argv; an unrecognized source refuses the same way. A missing binary is 400 `not_installed`.
- Source classification: symlink-resolved paths decide npm/pnpm (`node_modules[/.pnpm]`), bun (`/.bun/` — checked before the plain `node_modules` rule because bun's global layout is `~/.bun/install/global/node_modules`), and brew (`/Cellar/`, `/opt/homebrew/`, `/home/linuxbrew/`). pnpm 12 global bins are sh shim files in `.../pnpm` or `.../pnpm/bin`. When the resolved target classifies unknown, the PATH entry itself is tried — script installers (codex, claude) symlink `~/.local/bin/<name>` into private version dirs.
- `GET /api/v1/agents/{id}/job` returns the job envelope; `idle` when nothing ever ran.
- Job lifecycle is running → installing → verifying → succeeded | failed | interrupted, with output tail-capped to 4096 bytes and a 15-minute job timeout. Verify probes `binary --version` at 5s and, only when that probe died on its own deadline, retries once at 60s (hermes bootstraps ~11s on first run, ~0.1s warm). Daemon shutdown cancels active jobs to `interrupted`.

## How to get to it (user POV)

A user asks Prism to install or update an agent binary so the integrations screen can configure it. They either run `prismctl agents install codex`, or an app UI hitting the same routes. They watch the job state, and after `succeeded` the binary answers `--version`.

## Driving it with the harness

Two layers, both isolated from the machine's global state:

```sh
# routing and lifecycle proof: isolated prismd + sandbox PATH, fake tools,
# fake agent binaries, curl-only assertions, ~30 seconds
bash verify/scripts/agents-drive.sh

# real-userland proof: disposable docker containers, real npm/bun/pnpm/brew
# and real vendor install scripts, full install→verify→update→classify cycles
bash verify/scripts/matrix.sh build && bash verify/scripts/matrix.sh all
```

The sandbox drive boots an isolated prismd with fake `npm`, `bash`, and `sh` tools plus fake agent binaries under `<sandbox>/.local/bin`, so a real prismd runs real command execution through a fake environment. It asserts the list route renders all eight agents with `source: script` for the sandbox binaries; install of codex returns 202 and the job reaches `succeeded` with `npm install -g @openai/codex` in the command field; update of grok runs the `grok update` self-update; update of an unknown id is 404; and the `job` route returns `idle` for a never-driven agent.

The container matrix (`verify/scripts/`, evidence in `~/prism-agenttest-evidence/`, report in `AGENTTEST-REPORT.md`) proves the same contract against real userlands: npm install/update for all seven npm agents, bun install/update for omp, pnpm v12 classification and update, brew on ARM64 Linux, official curl-installer runs for codex/claude/grok/opencode, and a clean-ubuntu not-installed → install path. 15 of 16 cells pass; the known exception is documented in the report.

For a real-machine status proof (read-only, safe): `curl -sf http://127.0.0.1:18787/api/v1/agents | jq '.agents[] | {id, installed, source}'` against the desktop-supervised daemon. Never drive a real install from a verification run without the operator asking: these are 300MB-plus downloads into the user's global state.

## Gotchas

- Install state is PATH-derived: a stopped daemon and a fresh daemon report the same status. Never assert a job's existence from a previous session.
- `~/.local/bin` classifications: pi installed via npm under `~/.local` still classifies as `npm` (its resolved path contains `node_modules`), while script-installed codex under the same directory classifies as `script` even though its symlink target (`~/.codex/bin/...`) is unrecognized — the PATH entry is the fallback. Source detection reads the symlink-resolved path first.
- pi has no script plan, deliberately: pi's installer requires Node.js 22.19+ and npm, but the script plan is only reachable when npm is absent, so it can never succeed. With npm absent, pi install honestly returns `unsupported`.
- A second OpenCode install or update job returns 409 while the first job runs.
- The `force` flag applies only to methods that honor it; script reruns are idempotent and take no force argument.
- `hermes update --yes` is a git-pull-based updater: it rewrites the git checkout hermes was installed from. In the sandbox the fake runner intercepts it, so the drive proves routing, not hermes' real updater; the container matrix proves hermes' real npm install/update.
- The verify step runs the resolved binary name, not an absolute path: a PATH change between install and verify fails verify honestly. First-run bootstraps slower than 5s (hermes) get exactly one 60s retry.
- Installers that demand a TTY ("No terminal detected" under a pipe) would fail in `ExecRunner`, which has no pty; no current plan hits this, but a future script plan might.
