# Management views bridge

The renderer's workflow views (Providers, Accounts/Usage, Stats, Logs, Integrations, Machines) all reach the daemon through the same narrow bridge: `window.prism.management.call({method, path})` — the exact path the React UI takes. The bridge allowlists GET/POST/PUT/DELETE, requires paths under `/api/v1/`, and only permits `expectedGeneration`/`session` query keys. This file covers the bridge itself and the view-level wiring the user crosses.

## Sub-features

- Single bridge: `prism:management-request` IPC -> `validateManagementCall` -> main-process fetch (10s timeout) -> `{ok, status, body|error}`.
- Path validation: safe segments only, no `.`/`..` traversal, non-empty query values, whitelisted query keys.
- View wiring: each view's user actions map 1:1 onto management routes (see the per-feature files for the endpoint map).
- Error surfacing: `ApiError` carries the daemon `code`; views render `code: message` plus re-fetch buttons on CAS errors.
- Trusted sender: every IPC handler rejects destroyed/null senders.

## How to get to it (user POV)

Hash navigation: `#/providers`, `#/usage`, `#/stats`, `#/logs`, `#/integrations`, `#/machines`. Each view renders its own cards and tables; every button behind them writes through this bridge.

## Driving it with the harness

Follow SKILL.md Drive. Every mutation goes through the management bridge:

```js
await window.prism.management.call({method:'POST', path:'/api/v1/providers', body:{...}})
await window.prism.management.call({method:'PUT', path:'/api/v1/providers/test-up', body:{...}})
await window.prism.management.call({method:'DELETE', path:('/api/v1/providers/test-up' + '?expectedGeneration=' + gen)})
```

Proof combines the bridge reply (status 200/204, generation bump) with the read-back: `GET /api/v1/providers` must reflect the write (model enable/disable shows up in `disabledModels`, and `/v1/models` resolution drops disabled entries). Deleting a provider referenced by a route must fail with `invalid_document` until the route is removed.

Negative bridge proof: a path outside `/api/v1/`, a query key outside the two allowed, a body on GET/DELETE, or a non-object request all throw from `validateManagementCall` before any network — assert the thrown message, not an HTTP status.

## Gotchas

- Generation CAS: config writes need the current `expectedGeneration` (409 `stale_generation`); account pause/resume/priority send `{version}` (409 `stale_version`); account remove takes no CAS at all. The UI surfaces a re-fetch button on those errors.
- The CDP evaluator wraps expressions with a comma inside `(...)`: `'(window.location.hash = "#/models", "ok")'` — plain assignment with `;` fails the wrapper.
- Shell heredocs: `?` in an inline JS string can be glob-expanded by the shell; build the query string by concatenation.
- A provider whose route still references it cannot be deleted (400 `invalid_document`) — remove the route first.
- The Auth view reads `AccountsView` and `ProvidersView` filtered to the provider whose `wire === entry.provider`; an empty result renders "No accounts for {provider} yet."
- The bridge never surfaces raw credentials: provider reads show masked state, and auth status carries only opaque session ids and state names.
