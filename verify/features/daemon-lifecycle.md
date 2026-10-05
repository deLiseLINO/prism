# Daemon lifecycle

The desktop starts or reuses the registered background service and polls its health. Closing the window hides it. Quitting the app releases supervision but leaves the shared daemon running.

## Sub-features

- Startup calls `prism service start` with the selected listen address, config, and web UI directory. A healthy registration of the same version is reused.
- The registration is `$HOME/.prism/daemon.json`. It contains `id`, `pid`, `url`, and `version`. Health must report the same identity.
- The desktop moves through `starting`, `ready`, `backoff`, and `failed`. Three missed health polls trigger recovery. Backoff is bounded, and a stable ready interval resets the retry count.
- The Overview card renders the supervisor state and endpoint. Its chip maps `ready` to `operational`. The desktop status does not expose a daemon PID; read the registration and health response for ownership checks.
- App quit calls `release()`, which cancels supervision timers. `prism service stop` is the explicit command that stops the registered service.
- Detached daemon output goes to `$HOME/.prism/prism.log`, not the Electron profile directory.

## How to get to it

Launch prism and open Overview. Close the window to leave the app resident. Use the tray Quit action or the platform quit shortcut to exit the app. Use `prism service status` and `prism service stop` for the background service.

## Driving it with the verification scripts

```sh
bash verify/scripts/desktop-controls.sh
bash verify/scripts/integration-drive.sh
```

The first script matches the Overview state and endpoint against the isolated daemon. It terminates its owned Electron process, verifies the same daemon identity remains healthy, and explicitly stops that service. This proves process cleanup, not a native tray Quit click.

The integration script also exercises recovery. The shared runtime attests registration, health, executable, and listener ownership before terminating the daemon. Recovery must produce a new identity and restore the enabled integration config. A response from the old process is not recovery.

```sh
bash verify/scripts/owned-runtime-proof.sh
```

The runtime proof checks foreign-listener refusal, registered service identity, repeated stop, evidence durability, and publication failure handling.

## Gotchas

- Never discover an owner by process name or kill a process because it holds a port. Refuse a pre-existing listener.
- An isolated `HOME` and Electron profile keep verification separate from another app instance. Do not stop another instance to make the test run.
- Stop Electron before stopping the daemon. An active supervisor can restart a daemon during cleanup.
- A successful app quit does not imply service shutdown. A headless SIGTERM does not exercise `app.quit()` or the native tray.
- Process checks on Unix are not atomic kernel handles. The runtime rechecks identity before signaling and refuses mismatches.
- The product default port is 10200. Verification scripts select separate ports and reject daemon or CDP collisions.
