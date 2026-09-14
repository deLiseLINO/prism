# Renderer status

The React window subscribes to daemon status through the preload bridge. The sidebar footer shows a state dot, the state string, and the pid on every route; Overview renders the state chip and endpoint on the daemon card; and the tray tooltip mirrors the same stream.

## Sub-features

- Live status subscription: `window.prism.daemon.onStatus` pushes every supervisor transition into the UI.
- Initial fetch: `window.prism.daemon.status()` seeds the UI before the first push arrives.
- Sidebar footer: `daemon <state>` with a tone dot (pulsing when ready, danger when unreachable) and the pid when known. Hidden below the compact-width breakpoint.
- Overview daemon card: state chip plus `state`/`endpoint` rows. This is the only place the endpoint renders in the UI.

## How to get to it (user POV)

Launch the app. The window titled Prism opens Overview and shows the daemon card. The sidebar footer shows the daemon state and pid on every route. A clean or unknown hash falls back to Overview.

## Driving it with the harness

```sh
bash verify/scripts/desktop-controls.sh
```

`desktop-controls.sh` proves the unknown-hash fallback through the real UI: it navigates to `#/no-such-view` and requires Overview to render. For state equality, attach over CDP and read both the bridge and the DOM:

```js
await window.prism.daemon.status()    // the state the UI is fed
document.body.innerText               // what the user actually sees
```

Proof: the DOM text contains the same state string the bridge reports, and the endpoint port matches `PRISM_PORT`. For a live-update proof, stop the supervised child by PID (see daemon-lifecycle.md) and re-read the DOM: the state string must change without a page reload.

## Gotchas

- The renderer is sandboxed with a strict CSP: no inline script eval, no remote resources. Screenshot evidence comes from CDP `Page.captureScreenshot`, not from page-triggered downloads.
- `document.body.innerText` is only meaningful after the initial status fetch resolves; race it by waiting for the bridge promise first.
- The window close button hides, it does not quit; the DOM stays attached and the subscription keeps flowing.
- Unknown hashes fall back to Overview (`viewFromHash` default), so navigating to `#/nonsense` is a valid way to prove the fallback, not a 404 state.
- The endpoint renders only on the Overview daemon card; the sidebar footer carries state and pid. Do not look for endpoint text outside Overview.
