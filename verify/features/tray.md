# Tray

The desktop tray icon is the app's always-present handle in non-headless mode. Its tooltip shows the live daemon state and its menu offers Show and Quit. Closing the window leaves the app resident in the tray with the daemon running.

## Sub-features

- Tray icon: template image from `resources/trayTemplate.png` with a `trayTemplate@2x.png` variant. Both ship via `electron-builder.yml`.
- Initial tooltip `Prism`.
- Live tooltip. Every supervisor status emission rewrites it to the product name followed by `daemon ${status.state}`.
- Menu `Show Prism`, separator, `Quit Prism`.
- Show Prism menu item shows and focuses the main window.
- Quit Prism menu item calls `app.quit()`. `before-quit` releases supervision and the app exits. The registered background daemon remains running.
- Tray click (no menu) does the same show-and-focus as the menu item.
- Window close is intercepted and hides the window unless the app is quitting. `window-all-closed` is a no-op so the app stays resident.
- Headless runs construct the tray controller but never `init` it and never call `update`.

## How to get to it (user POV)

The icon appears in the platform tray or menu bar at launch. Click it to show the window. Hover to read the daemon state. Open the menu to Quit. The window close button only hides it and the daemon keeps serving.

## Driving it with the harness

Tray menu items are Electron `Menu` objects, not renderer DOM, so CDP cannot click them. Drive the tray's effects, not its pixels. Headless runs (`PRISM_HEADLESS=1`, the harness default) skip tray init entirely, so tray assertions apply only to headed runs.

1. Cleanup scripts terminate their owned Electron process, require the same daemon identity to remain healthy, then explicitly stop that registered service. This does not exercise `app.quit` or the native Quit item.
2. The daemon serves the desktop drive while Electron remains alive. CDP can inspect the renderer and health throughout. This does not prove that the native close button hides the window or that a tray action shows it.
3. Tooltip state: read the status stream the tooltip mirrors with `await window.prism.daemon.status()`. Assert the sidebar footer text `daemon <state>` shows the same `state` string. Both are fed from the same supervisor emission in `main/index.ts`.

Do not assert tray rendering via screenshots. The menu bar is outside the renderer's DOM and outside CDP's reach.

## Gotchas

- There is no tray Start or Stop action. Supervision ends on app quit, while the background service remains available.
- `window-all-closed` is deliberately a no-op, so a real quit must go through the tray menu or Cmd+Q.
- Second-instance launch and dock `activate` also show the window, and showing is a no-op when headless.
- The tooltip updates only when the supervisor emits a status change. A silent healthy daemon keeps the last state string, which is correct.
- The sidebar footer reads `daemon …` while status is still loading.
- Headless verification does not initialize or visually prove the platform tray.
