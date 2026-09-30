# Machines and remote host switching

The Machines screen lists this machine and every host in the daemon's `hosts` config section. The user can add a machine, remove it, and Manage it. Manage flips the whole app to that machine's daemon. A banner shows which machine is active and offers a way back.

## Sub-features

- Machines is a left navigation entry (`#/machines`). It is visible only when the `remoteInstall` experimental flag is on.
- The add form takes `Name` and `SSH address` and has an `Add machine` button. The daemon probes the host over SSH and reports an honest `ok` or `unresolved` status. An unresolved host is kept, not rolled back, and shows the ssh error as detail.
- `Remove` asks for confirmation. On confirm the config entry, the host table entry, and the reverse tunnel go away without a daemon restart.
- The local row shows `this machine` and a `Manage here` button. A remote row shows `Manage`. The active row's button reads `Managing`.
- `Manage` sets a global active host. Every view remounts for that host, and all calls go to the remote daemon. The renderer attaches the host to each bridge call, and the main process forwards it over an SSH port forward to the remote prismd (default port 10200). Integrations use the same unscoped routes, so the list, paths, Apply, and Rollback all act on the remote machine.
- While a remote host is active, a banner reads `Managing <host>` with a `Back to this machine` button. Clicking it restores local data.
- A remote row without a daemon port shows `Install prismd` (flag-gated). A failed install shows `Retry install`.
- Hosts with a daemon port are external. They are listed as ok without an SSH probe.
- The scoped routes `/api/v1/hosts/{host}/integrations` still exist on the daemon. The desktop UI does not use them. An unresolved host on those routes answers 503 `host_unavailable`.
- There is no host selector on the Integrations screen.

## How to get to it (user POV)

Enable the `remoteInstall` experimental flag. Click `Machines` in the left navigation. Fill `Name` and `SSH address`, then click `Add machine`. The machine appears with its live status. Click `Manage` on its row. The banner `Managing <host>` appears and Providers, Integrations, and the other views show that machine's data. Click `Back to this machine` to return.

## Driving it with Electron CDP

- `scripts/machines-proof.sh` drives the add/remove lifecycle. It uses the real form and the real Remove button plus confirm. It cross-checks `GET /api/v1/hosts` and the persisted config file, and requires the host to vanish after removal. The proof host is `self` pointing at `localhost`.
- `scripts/remote-switch-proof.sh` drives the switching contract. It starts two prismd instances, seeds a host with a daemon port, clicks `Manage`, asserts the `Managing <host>` banner and remote-only data on Providers, then clicks `Back to this machine` and asserts local data returns.
- Both need key-auth loopback `ssh localhost`. Both refuse to run if a daemon already holds their port.

## Gotchas

- Machines and its Manage buttons do not exist until the `remoteInstall` flag is on. Enable it before driving the UI.
- The Machines form always talks to the local daemon, even while a remote host is active.
- A host whose SSH probe fails is listed as `unresolved` with the ssh error as detail. Managing it fails through the bridge rather than falling back to local data.
- Proof hosts such as `self` point at the same machine. The reverse tunnel supervisor then fights the daemon for the port, so expect reconnect log noise in `prismd.log`. That is a known artifact, not a failure.
- The host list syncs on a 30 second interval in the main process. A newly added host may take a moment to become routable.
