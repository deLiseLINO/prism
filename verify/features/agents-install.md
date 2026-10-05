# Client install and update

prism provides explicit installation, update, and owner-preserving reinstall for its existing integration clients. Management routes and `prismctl agents` start asynchronous jobs.

## Management contract

- `GET /api/v1/agents` lists integrations with `installed`, `source`, `path`, `canUpdate`, `reason`, and the last `job`.
- `GET /api/v1/agents/{id}` returns one status. An unknown id returns 404 `not_found`.
- `POST /api/v1/agents/{id}/install` installs an absent client or repairs a present client through its proven owner.
- `?force=true` adds `--force` to npm and Bun install commands. Homebrew repairs use the observed formula or cask.
- `POST /api/v1/agents/{id}/update` updates the selected installation. A missing client returns 400 `not_installed`.
- An admitted job returns 202. Unsupported maintenance returns 200 with a terminal `unsupported` job and a reason.
- Concurrent jobs for the same binary or maintenance root return 409 `install_active`.
- `GET /api/v1/agents/{id}/job` returns the last job. A client without a previous job has state `idle`.

Jobs can include `beforeVersion`, `expectedVersion`, `version`, `verification`, and `note`. `verification` distinguishes a known release, client verification without a numeric release, and an installed executable.

## Discovery and maintenance authority

Discovery honors an explicit executable override before it searches PATH, known installation directories, and supported toolchain directories. With several executable copies, the newest parsed semantic version wins. Equal versions retain deterministic directory order. Each admitted job freezes its selected entry and owner.

Discovery does not grant permission to mutate a client. npm and Bun repairs require matching global package manifests and executable links. Package repairs preserve their observed root and package alias. Unknown project packages and opaque wrappers require manual maintenance.

Homebrew maintenance requires a versioned Cellar or Caskroom layout and the serving prefix's brew executable. The observed safe formula or cask token determines the operation. Release metadata must identify that same token.

Supported native and global pnpm installations can use the selected client's declared updater without gaining reinstall authority. Known launcher layouts for client Hermes and client Claude are recognized. Client OpenCode uses `upgrade`. Client Grok uses `version` for its version probe. Client Pi uses `update --self`; supported managed installations require a valid marker and matching launcher.

npm installations of codex require manual Update. A recognized standalone codex installation updates through a verified release archive. Unsupported standalone layouts remain manual. Native reinstall for client OpenCode, client Hermes, and client Pi remains limited to proven repair contracts.

## Installation and release verification

Shared client installers prefer their supported shell method before npm. Client Pi's npm method uses `--ignore-scripts`. Installer prerequisites and commands use the same resolved tool environment.

Fresh script installation verifies a new or changed executable at its intended destination. A different existing copy cannot satisfy the operation. This does not identify which external actor wrote the exact intended path during installation.

Update checks the expected release before mutation and freezes that expectation. An already-current client can finish without mutation. Verification probes the selected entry and checks its stable owner before and after the probe. A measured version below the expected release fails.

Client Hermes can update commits without changing its CLI version. Client Claude with a successful doctor result but no identifiable channel records weaker client verification. A failed doctor or failed release request does not silently become weaker proof.

Standalone codex updates validate the release tag, asset name, HTTPS URL, size, SHA-256 digest, archive paths, package manifest, and staged executable. The installation lock remains held through atomic activation and verification. Old releases are retained. Postactivation failure is reported without automatic rollback.

## Process lifetime and diagnostics

On macOS and Linux, the Runner owns a private process group and waits for ordinary descendants to stop before it returns. Operation locks remain held until cleanup finishes. Shutdown closes admission, cancels work, and reports incomplete cleanup if its caller's budget expires. Deliberate session escapes are not contained by process-group ownership.

Installation removes internal `PRISM_*` environment keys, sets `CI=1`, `NONINTERACTIVE=1`, and `TERM=dumb`, and supplies null stdin. Maintenance diagnostics mask supported credential formats before retaining a bounded 4096-byte tail. stdout and stderr have independent partial-line buffers.

Version probes have a 5-second timeout and one 60-second retry only after their own deadline expires. Jobs have a 15-minute deadline. Postactivation standalone verification can outlast the shutdown caller's budget while retaining its lock.

## UI and CLI

The experimental `agentActions` flag controls desktop Install, Reinstall, and Update buttons. Daemon routes and CLI commands do not require that flag.

`prismctl agents status`, `install`, `update`, and `job` use the management routes. Immediate and polled unsupported outcomes display their refusal reasons in the desktop UI.

## Verification scripts

`verify/scripts/agents-drive.sh` builds a daemon and exercises API and CLI installation, reinstall, manual npm Update, and a supported release transition.

`verify/scripts/agents-ui-drive.sh` exercises the same operations through rendered desktop controls and captures job evidence and screenshots.

Both drivers use temporary client fixtures, explicit executable overrides, a clean environment, and the macOS daemon sandbox from `agents-sandbox.sh`. They deny genuine fixed-directory clients and writes outside the fixture directory. They refuse occupied ports and fail closed on unsupported platforms.

These drivers do not mutate genuine clients. Fixture checks do not establish every vendor updater's destination behavior. Linux runtime and future managed-marker layouts require separate verification.
