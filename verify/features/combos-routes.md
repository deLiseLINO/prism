# Combos and routes

The Combos & Routes view manages weighted model combos and the route aliases that map inbound model names to combos or provider/model pairs. Both families write under the generation CAS.

## Sub-features

- Combos: create (`New combo` opens an editor whose submit reads `Create combo` / `Save combo`), edit, delete; each combo has strategy (`failover`, `round_robin`), sticky limit, alias, native alias, display name, image-input toggle, and a targets list (`provider/model` with weight).
- Targets sub-card: provider select, model select (options from the current providers), weight number, Remove, Add target.
- Routes: `Set route` (key -> combo id or provider/model), per-row editable value with Save, Delete per row.
- Backing endpoints: `GET /api/v1/combos` and `GET /api/v1/routes` list; `PUT/DELETE /api/v1/combos/{id}` and `PUT/DELETE /api/v1/routes/{key}` mutate. PUT takes `expectedGeneration` in the JSON body, DELETE takes it as `?expectedGeneration=N`. A single-item GET answers 405.
- Stale generation: 409 `stale_generation` renders a `Re-fetch routes`/`Re-fetch configuration` button.
- Search filters combos by id/provider/model and routes by key/value.

## How to get to it (user POV)

Click `Combos & Routes` (`#/combos`). Combo cards render targets as `provider/model` with weight badges; the Routes card at the bottom holds the alias map.

## Driving it with the harness

```sh
bash verify/scripts/api-sweep.sh
bash verify/scripts/prismctl-proof.sh
```

`api-sweep.sh` proves the HTTP ladder: PUT a throwaway combo and route under `expectedGeneration` CAS, read both back from the list endpoints, delete the route then the combo, and 409 on a stale generation. `prismctl-proof.sh` proves the CLI ladder (`combos set/remove`, `routes set/remove` with read-back). For the UI rendering, follow SKILL.md Drive through the management bridge with the generation in every write:

```js
const gen = (await window.prism.management.call({method:'GET', path:'/api/v1/combos'})).body.generation
const comboRes = await window.prism.management.call({method:'PUT', path:'/api/v1/combos/verify-combo', body:{targets:[{provider:'codex', model:'gpt-5.6-luna', weight:1}], strategy:'failover', stickyLimit:2, expectedGeneration:gen}})
await window.prism.management.call({method:'PUT', path:'/api/v1/routes/verify-alias', body:{value:'verify-combo', expectedGeneration:comboRes.body.generation}})
```

After the writes, `GET /api/v1/combos` and `GET /api/v1/routes` reflect them, the combos UI renders the new target and weight, and `GET /v1/models` lists `verify-alias`. Delete the route first, then the combo (a combo referenced by a route blocks combo deletion the same way a provider referenced by a route blocks provider deletion). Clean up both in the same run.

## Gotchas

- Route values must be `provider/model` or a combo id; a bare model name is a 400. Route values are validated against known providers and models.
- Combos are full replaces: a PUT with a missing `targets` entry deletes it. The UI's Add target exists to prevent that; the bridge does not.
- Weight 0 is legal (target present but never first choice) — do not "fix" it during verification.
- The combos UI's per-row Save writes a RouteWrite with the current generation; two rapid saves race the CAS and the second returns `stale_generation`. Re-fetch between writes.
- `stickyLimit` 0 means no sticky pinning, not "unlimited" — pick the value deliberately in assertions.
