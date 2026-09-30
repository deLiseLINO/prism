# Management views bridge

The renderer's workflow views (Providers, Accounts/Usage, Stats, Logs, Integrations, Machines) all reach the daemon through the same narrow bridge: `window.prism.management.call({method, path, body?, host?})`, the exact path the React UI takes. The bridge allowlists GET/POST/PUT/DELETE, requires paths under `/api/v1/`, and only permits the query keys `expectedGeneration`, `session`, `force`, `range`, `limit`. The optional `host` field routes the call to another machine's daemon. Empty and `local` mean this machine. This file covers the bridge itself and the view-level wiring the user crosses.

## Sub-features

- Single bridge: `prism:management-request` IPC -> main-process `validateManagementCall` -> host routing -> fetch (10s timeout) -> `{ok, status, body|error}`. Network failure or timeout returns `{ok:false, status:0, error}`.
- Path validation: safe segments only, no `.`/`..` traversal, non-empty query values, whitelisted query keys, safe `host` identifier (`^[A-Za-z0-9_.-]+$`). Validation runs in the main process, not preload.
- View wiring: each view's user actions map 1:1 onto management routes (see the per-feature files for the endpoint map).
- Error surfacing: `ApiError` carries the daemon `code`; views render `code: message` plus a `Re-fetch configuration` button on `stale_generation`. Non-daemon errors get code `unknown`.
- Trusted sender: every IPC handler rejects destroyed/null senders.

## How to get to it (user POV)

Hash navigation: `#/overview`, `#/providers`, `#/usage` (Accounts), `#/stats`, `#/logs`, `#/integrations`, `#/machines`, `#/experimental`. Unknown hashes such as `#/models` fall back to Overview. Each view renders its own cards and tables; every button behind them writes through this bridge.

## Driving it with the harness

Follow SKILL.md Drive. Every mutation goes through the management bridge:

```js
await window.prism.management.call({method:'POST', path:'/api/v1/providers', body:{...}})
await window.prism.management.call({method:'PUT', path:'/api/v1/providers/test-up', body:{...}})
await window.prism.management.call({method:'DELETE', path:('/api/v1/providers/test-up' + '?expectedGeneration=' + gen)})
```

Proof combines the bridge reply (status 200/204, generation bump) with the read-back: `GET /api/v1/providers` must reflect the write (model enable/disable shows up in `disabledModels`, and `/v1/models` resolution drops disabled entries). Deleting a provider returns 200 with the bumped generation and prunes routes, aliases and combo targets prefixed `<id>/`. `GET /api/v1/routes` must no longer list them. An unknown id returns 404 `not_found`.

Negative bridge proof: a path outside `/api/v1/`, a query key outside the five allowed or with an empty value (`?redirect=x`, `?expectedGeneration=`), a body on GET/DELETE, `host:'a/b'`, or a non-object request all reject before any network. The invoke promise rejects with an Electron-wrapped `prism: management ...` message. Assert a substring, not an HTTP status. Messages include `management path must be a safe /api/v1/ route`, `management method must be one of GET, POST, PUT, DELETE`, `management body requires POST or PUT`, `management host must be a safe host identifier`, `management request must be an object with method and path`. The daemon log (`<userData>/prismd.log`) must show no request for these.

## Gotchas

- Generation CAS: config writes need the current `expectedGeneration` (409 `stale_generation`); the UI shows `Re-fetch configuration` on `stale_generation`. The account pause/resume/priority routes still exist in the API client and daemon (409 `stale_version`) but no view calls them. Account remove takes no CAS. Account pinning in the UI is a provider PUT with `pool.pinnedAccount`.
- The CDP evaluator wraps expressions with a comma inside `(...)`: `'(window.location.hash = "#/providers", "ok")'` — plain assignment with `;` fails the wrapper.
- Shell heredocs: `?` in an inline JS string can be glob-expanded by the shell; build the query string by concatenation.
- Deleting a provider silently prunes its routes, aliases and combo targets. Read `/api/v1/routes` back to prove it.
- The Accounts view (`#/usage`) empty states are `No accounts yet. Add one to start routing.` and `No accounts match the filter.`
- The bridge never surfaces raw credentials: provider reads show masked state, and auth status carries only opaque session ids and state names.
