# Tray

The desktop tray icon is the app's always-present handle in non-headless mode: it shows the live daemon state in its tooltip and offers Show and Quit. Closing the window leaves the app resident in the tray with the daemon running.

## Sub-features

- Tray icon: template image from `resources/trayTemplate.png`, tooltip `Prism`.
- Live tooltip: `Prism — daemon ${status.state}` updated on every supervisor status emission.
- Show Prism menu item: shows and focuses the main window.
- Quit Prism menu item: triggers the full quit path — `before-quit` stops the daemon first (`stopForQuit`), then the app exits.
- Tray click (no menu): same show-and-focus as the menu item.
- Window close: hidden, not closed; `window-all-closed` is a no-op so the app stays resident.

## How to get to it (user POV)

The icon appears in the platform tray or menu bar at launch. Click it to show the window; hover to read the daemon state; open the menu to Quit. The red close button on the window only hides it — the daemon keeps serving.

## Driving it with the harness

Tray menu items are Electron `Menu` objects, not renderer DOM, so CDP cannot click them. Drive the tray's effects, not its pixels. Headless runs (`PRISM_HEADLESS=1`, the harness default) skip tray init entirely, so tray assertions apply only to headed runs:

1. Quit lifecycle: harness scripts SIGTERM Electron, wait, and require health to fail. This proves process cleanup, but not the tray menu item's callback wiring.
2. Show path: `scripts/desktop-controls.sh` keeps the app resident through its whole drive — the daemon serves every UI interaction and health answers throughout, and only the final quit (SIGTERM, the tray Quit equivalent path) tears it down. Assert through `document.title` remaining readable over CDP and health still answering.
3. Tooltip state: read the status stream the tooltip mirrors — `await window.prism.daemon.status()` and assert the DOM banner shows the same `state` string; the tooltip is fed from the identical emission in `main/index.ts`.

Do not assert tray rendering via screenshots: the menu bar is outside the renderer's DOM and outside CDP's reach.
## Gotchas

- There is no tray Start/Stop action and no manual lifecycle control anywhere; the daemon is supervised from app start to app quit. Do not invent tray menu items that do not exist.
- `window-all-closed` is deliberately a no-op (tray-resident app), so quitting must go through the tray menu or Cmd+Q.
- The tray tooltip updates only when the supervisor emits a status change; a silent daemon (healthy, no transitions) keeps the last state string, which is correct behavior, not a stuck tooltip.
- The template image lives at `resources/trayTemplate.png`. Headless verification does not initialize or visually prove the platform tray.
