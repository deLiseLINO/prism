# Machines and remote host switching

The Machines screen lists this machine and every host in the daemon's `hosts` config section. Every remote machine runs its own prismd. The user adds a machine, the desktop installs prismd there, and the main daemon keeps that machine's settings in sync with its own. Manage flips the whole app to that machine's daemon. A banner shows which machine is active and offers a way back.

## Sub-features

- Machines is a left navigation entry (`#/machines`). It is visible only when the `remoteInstall` experimental flag is on. With the tab visible, install works without any other gate.
- The add form takes `Name` and `SSH address` and has an `Add machine` button. The daemon saves the host without contacting it, so a new host is listed as `not installed`. Right after the save the desktop starts the install for that host on its own.
- The install streams the bundled prism binary over SSH to `~/.prism/remote/prism`, starts it on the remote loopback port (default 10200), checks its health, and registers the port with `PUT /api/v1/hosts/{host}`. A daemon that already answers on that port is adopted instead. The row shows `Installing…` while it runs. A failure shows the reason under the row and a `Retry install` button. A `not installed` row always offers `Install prismd`.
- A host is `installed` once its daemon port is registered. Only installed rows can be managed; `Manage` stays disabled on `not installed` rows.
- Each installed remote row has an `Auto sync` switch, on by default. Clicking anywhere on the setting row toggles it. The switch calls `PUT /api/v1/hosts/{host}/auto-sync` with the current config generation and then reloads the list. The control is disabled while the request runs, and a failure shows inline under the row.
- Next to the switch the row shows the sync status: `Synced <time>` after a successful push, `Sync failed: <error>` with the full error on hover, `Waiting for first sync` before the first attempt, and `Auto sync off` when the switch is off. While a sync is pending the list reloads every few seconds.
- The main daemon pushes its config to every installed host with auto sync on. It pushes when the config changes, when a host is installed or registered, and on a timer. The push goes over SSH to the remote daemon. It carries providers with their API keys, models, combos, routes, aliases, settings, and the integration enabled flags. The remote daemon then applies the same agent integrations on its own machine.
- OAuth accounts (browser login) are not synced. Accounts belong to each machine; the user logs in on a remote machine through Manage.
- `Remove` asks for confirmation. On confirm the config entry goes away without a daemon restart and the main daemon stops syncing to that machine. The remote daemon and the config it already has stay as they are.
- The local row shows `this machine` and a `Manage here` button. A remote row shows `Manage`. The active row's button reads `Managing`.
- `Manage` sets a global active host. Every view remounts for that host, and all calls go to the remote daemon. The renderer attaches the host to each bridge call, and the main process forwards it over an SSH port forward to the remote prismd. Integrations use the same unscoped `/api/v1/integrations*` routes, so the list, paths, Apply, and Rollback all act on the remote machine's own daemon.
- While a remote host is active, a banner reads `Managing <host>` with a `Back to this machine` button. When that host has auto sync on, the banner adds `Settings sync from this machine. Changes made here are replaced on the next sync.` Clicking `Back to this machine` restores local data.
- There is no reverse tunnel and no host-scoped integration route. The main daemon never edits client config files on a remote machine.
- There is no host selector on the Integrations screen.

## How to get to it (user POV)

Enable the `remoteInstall` experimental flag. Click `Machines` in the left navigation. Fill `Name` and `SSH address`, then click `Add machine`. The machine appears and its install starts. When it finishes the row shows `installed`, the `Auto sync` switch is on, and the status turns to `Synced` after the first push. Click `Manage` on its row. The banner `Managing <host>` appears with the sync note, and Providers, Integrations, and the other views show that machine's data. Click `Back to this machine` to return.

## Driving it with Electron CDP

- `scripts/machines-proof.sh` drives the add/remove lifecycle. It uses the real form and the real Remove button plus confirm. The proof host is `self` pointing at an unresolvable name. It asserts the host is saved as `not_installed`, that the install starts on its own, fails at the SSH probe with the reason shown and `Retry install` offered, and that `Manage` stays disabled. It cross-checks `GET /api/v1/hosts` and the persisted config file, and requires the host to vanish after removal.
- `scripts/remote-switch-proof.sh` drives switching and the sync switch. It starts two prismd instances and seeds a host with a daemon port and auto sync off. It clicks `Manage`, asserts the `Managing <host>` banner without the sync note and remote-only data on Providers, then clicks `Back to this machine` and asserts local data returns. It then clicks the `Auto sync` row, waits for `Synced`, checks that the remote daemon received this machine's providers, and asserts the banner note on the next `Manage`.
- `scripts/remote-install-proof.sh` adds `self` pointing at `localhost` through the form and asserts the install starts without a click. It switches tabs mid-install, waits for the install, checks the daemon port and the installed binary, waits for the first `Synced`, and manages the new daemon to see the synced providers.
- `remote-switch-proof.sh` and `remote-install-proof.sh` need key-auth loopback `ssh localhost`. All three refuse to run if a daemon already holds their port.

## Gotchas

- Machines and its Manage buttons do not exist until the `remoteInstall` flag is on. Enable it before driving the UI.
- The Machines form always talks to the local daemon, even while a remote host is active.
- Settings changed while managing a synced machine are overwritten by the next push. Turn auto sync off on that row first to keep them.
- `remote-install-proof.sh` installs into the real `~/.prism/remote` of the SSH target and removes it on exit.
- The host list syncs on a 30 second interval in the main process. A newly installed host may take a moment to become routable for Manage.
