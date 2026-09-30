# Renderer status

The React window subscribes to daemon status through the preload bridge. The sidebar footer shows a state dot, the state string, and the pid on every route. Overview renders the state chip, state and endpoint on the daemon card. The tray tooltip mirrors the same stream when the app is not headless.

## Sub-features

- Live status subscription: `window.prism.daemon.onStatus` pushes every supervisor transition into the UI.
- Initial fetch: `window.prism.daemon.status()` seeds the UI before the first push arrives.
- Sidebar footer: `daemon <state>` with a tone dot (pulsing when ready, danger when failed or unreachable, warn for transitional states) and `pid N` when known. Shows `daemon …` before the first fetch and `daemon unreachable` if the fetch rejects. Hidden at widths of 1040px or less.
- Overview daemon card: a chip plus `state` and `endpoint` rows. The chip reads `operational` when ready, `checking…` before the first fetch, `unreachable` on fetch failure, otherwise the state string. The endpoint row reads `unknown` before the first fetch and `not listening` when the daemon has no endpoint (idle).
- Boot gate: the main pane shows a boot screen (`starting daemon…`, `restarting (attempt N)…`, `daemon stopped`, `waking up…`) until the daemon first reports ready, with a 20s timeout. Overview content is not rendered before then.

## How to get to it (user POV)

Launch the app. The window titled Prism opens on Overview and shows the daemon card once the daemon has been ready. The sidebar footer shows the daemon state and pid on every route. An unknown hash falls back to Overview. On launch with an empty hash, the mount effect in `App.tsx` replaces the hash with `#/overview` even when `prism-view` holds another view, so the stored view is not restored at startup.

## Driving it with the harness

```sh
bash verify/scripts/desktop-controls.sh
```

`desktop-controls.sh` proves the unknown-hash fallback through the real UI: it navigates to `#/no-such-view` and requires Overview to render. For state equality, attach over CDP and read both the bridge and the DOM:

```js
await window.prism.daemon.status()    // the state the UI is fed
document.body.innerText               // what the user actually sees
```

Proof: the DOM text contains the same state string the bridge reports (the Overview chip shows `operational` for `ready`), and the endpoint port matches `PRISM_PORT`. For a live-update proof, stop the supervised child by PID (see daemon-lifecycle.md) and re-read the DOM: the state string must change without a page reload. The tray tooltip carries the daemon state and is not observable in a headless run.

## Gotchas

- The renderer is sandboxed with a strict CSP: no inline script eval, no remote resources. Screenshot evidence comes from CDP `Page.captureScreenshot`, not from page-triggered downloads.
- `document.body.innerText` is only meaningful after the initial status fetch resolves; race it by waiting for the bridge promise first.
- The window close button hides, it does not quit; the DOM stays attached and the subscription keeps flowing.
- Unknown hashes fall back to Overview (`viewFromHash` default), so navigating to `#/nonsense` is a valid way to prove the fallback, not a 404 state. `readCurrentView` returns the stored `prism-view` for an empty hash, but the App mount effect then navigates to Overview. A stored value that is not a known view falls back to Overview.
- The daemon endpoint renders on the Overview daemon card. The sidebar footer carries state and pid only. Integrations cards show a per-client endpoint, which is a different value.
- Wait for the boot gate to clear (daemon ready) before reading the Overview card; before that the main pane shows the boot screen.
