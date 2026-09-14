# Host switching in the Integrations view

The Integrations screen manages client configs on the local machine and on every host configured in the daemon's `hosts` config section. The user-facing contract: the host selector lists every host with an honest status label, switching reloads the integration list for that host, Apply and Rollback act on the selected host, and an unreachable host shows its statuses (or a refusal) rather than pretending to be local.

## Sub-features

- The Machines screen (`#/machines`) adds a remote host through a form (name + SSH address) with generation CAS; the daemon probes the host over SSH and reports an honest `ok` or `unresolved` status without rolling the config back.
- The Machines screen removes a host through Remove + confirm; the config entry, the host table entry, and the reverse tunnel all go away without a daemon restart.
- The `Host` selector in the Integrations screen head lists `This machine` (id `local`) first, then every configured remote host.
- Switching the selector reloads the whole integration list from `GET /api/v1/hosts/{host}/integrations` (the local host uses the unscoped route); paths in the rows point at the remote home.
- Apply and Rollback on a selected remote host hit `POST /api/v1/hosts/{host}/integrations/{client}/apply|rollback` and write the real file on the remote machine over SSH.
- An unresolved host refuses with the daemon's reason (`host_unavailable`, HTTP 503); the UI surfaces that refusal through the row alert instead of falling back to local statuses.

## How to get to it (user POV)

Launch Prism, click `Integrations` in the left navigation. The screen head shows a `Host` label and a select control. Pick a host from the dropdown; the integration rows reload for that host. Clicking Apply or Rollback on any row acts on the selected host.

## Driving it with Electron CDP

Use `scripts/hosts-proof.sh`. It builds prismd and the desktop, writes a sandbox daemon config with one host `self` pointing at `localhost`, launches isolated headless Electron, and then through the rendered UI:

1. clicks the `Integrations` navigation button;
## How to get to it (user POV)

Launch Prism, click `Machines` in the left navigation, fill `Name` and `SSH address`, click `Add machine`. The new machine appears in the list with its live status. `Remove` asks for confirmation and removes it. Then open `Integrations`: the host selector lists the new machine. Switching the selector reloads the integration list for that host, and Apply and Rollback act on the selected host.

## Driving it with Electron CDP

Use `scripts/machines-proof.sh` for the add/remove lifecycle: it drives the real form and the real Remove button, cross-checks the daemon hosts route and the persisted config file, and requires the tunnel-side teardown (host vanishes from `GET /api/v1/hosts`). Use `scripts/hosts-proof.sh` for the switching contract. Both need key-auth loopback `ssh localhost`.

## Gotchas

- The daemon resolves hosts once at startup; adding a host to the config requires a daemon restart before the UI can see it.
- A host whose SSH probe fails is listed as `unresolved` with the ssh error as detail; the row list still loads (statuses are honest) but Apply answers 503 `host_unavailable`.
- The select renders `local` as `This machine`; option values are host ids.
- `self`-style hosts (a host entry pointing at the same machine) are fine for proofs, but the reverse tunnel supervisor will fight the daemon for the port; expect reconnect log noise. That noise is the known artifact, not a failure.
- The hosts list API is `GET /api/v1/hosts`; the renderer falls back to a single `local` entry when it fails, so a broken hosts route looks like "no selector options" rather than an error screen.
