# Daemon lifecycle

The desktop app spawns prismd on boot, watches `/api/v1/health`, restarts it with bounded backoff if it crashes, and stops it on app quit. Closing the window does neither: the app hides to the tray and the daemon keeps running. There are no manual lifecycle controls: the daemon is always supervised from app start to app quit, and the Overview card reports its state.

## Sub-features

- Boot supervision: child spawn with `--listen`, health polling, state transitions to `ready`.
- Crash handling: unexpected child exit moves the state machine through `backoff` and restarts, bounded by max restarts (5). The first crash waits ~1000ms (`500 * 2^1`, attempt starts at 1); the 60s stability window resets the attempt to 0, and only then is the next backoff ~500ms.
- Status surface: the Overview daemon card renders `state` and `endpoint` from the same supervisor stream that feeds the sidebar pill and the tray tooltip. Read-only: the card has no buttons.
- Quit: app quit stops the child (SIGTERM, then SIGKILL after the 5s grace window) before the process exits.
- Window close vs quit: close hides the window; only quit stops the daemon.

## How to get to it (user POV)

Launch the app. It opens Overview, where the daemon card leads the grid and shows the state chip and endpoint. Quit via the tray menu or Cmd+Q.

## Driving it with the harness

```sh
bash scripts/verify/scripts/desktop-controls.sh
```

`desktop-controls.sh` proves the read surface end to end: waits for the `ready` state on the Overview card, requires the card's endpoint row to match `http://127.0.0.1:$PORT`, and asserts the health endpoint answers. At the end it quits Electron and requires health to refuse, proving teardown. Boot supervision and quit wiring stay covered by `integration-drive.sh` as well.

For the crash path, kill the supervised daemon by its PID (from `daemon.status()` -> `pid`) and watch the state machine react:

```js
await window.prism.daemon.status()   // read `pid` and `state`
// then SIGTERM that exact PID from the shell
await window.prism.daemon.status()   // attempt incremented, new pid, state back to 'ready'
// 'backoff' is ~1000ms after a fresh crash at default settings (500ms only after a 60s-stable run reset the attempt); asserting on it needs a tight poll
```

## Gotchas

- Single instance lock: a second `electron apps/desktop` exits immediately. Kill the previous instance first — and the quality gate refuses to run while any daemon already answers on the verification port (default 18787), because it will not double-drive a shared instance.
- After SIGTERM of the app, poll health until it refuses: the grace window gives the child up to 5 seconds to die.
- The `ready` state name is the app's contract; there is no `healthy` state (the health endpoint body is the one that says `ok`). The Overview chip renders `operational` for it; both strings prove readiness.
- The supervisor spawns `prismd --listen 127.0.0.1:<port>` with stderr inherited, so the daemon's own log lines land in the Electron app's stderr capture (`app.log` in evidence).
- Prismd's default port is 8787 when launched standalone; the desktop supervisor overrides it via `PRISM_PORT` (default contract 8787; verification uses 18787 to leave the user's instance alone).
- There is no manual Start/Stop anywhere in the UI or the bridge (`PrismBridge.daemon` is `status`/`onStatus` only). Do not script `window.prism.daemon.stop()` — it does not exist; kill the supervisor's child by PID for crash-path proofs.
