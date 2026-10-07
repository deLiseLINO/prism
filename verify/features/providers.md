# Providers

The Providers view is the CRUD surface for daemon providers: wires, endpoints, models, credentials, enablement, and pool policy. Every write goes through the generation CAS.

## Sub-features

- List: a rail (`aside.prov-rail`, one `.prov-prow` per provider with id, enabled toggle, wire label, model count, disabled-count line) beside a detail pane (`.prov-detail`) for the active provider. The detail head shows id, wire label plus base URL, and an Enabled/Disabled toggle. Wire labels are `openai-completions` (chat), `openai-responses` (responses), `anthropic` (messages), `cline`; codex and antigravity show their raw name. The generation stays in component state for CAS writes and is not rendered. Credential state is deliberately not rendered anywhere in the providers UI (rail, detail, editor).
- Create: `POST /api/v1/providers` with `{id, wire, ...}`; wires are `codex`, `antigravity`, `responses`, `messages`, `chat`, `cline`. The editor offers only the four in the wire picker (`openai-completions`, `openai-responses`, `anthropic`, `cline`).
- Replace: `PUT /api/v1/providers/{id}` with `expectedGeneration`; absent fields preserve existing values (absent=preserve merge).
- Delete: `DELETE /api/v1/providers/{id}?expectedGeneration=N` cascades in one generation write. It removes routes and aliases whose value starts with `<id>/`, drops that provider's combo targets, deletes combos left with no targets, then deletes the stored credential. Returns 200. The UI Delete button opens an inline Confirm ("Delete <id>?", confirm label `Delete`).
- Credentials: paste-once write (`credential` in the write body, or `apiKeyRef`); reads show only masked state (`set`/`unset`) in the API; the value never round-trips and the UI never renders the state.
- A credential replacement stages a private immutable key reference before publishing the generation-checked provider config. Failed staging, config persistence, or stale CAS leaves the active key, config, and account policy unchanged. Rejected accounts resume only after successful replacement; explicit user pauses remain paused, including after restart. Old-key failures cannot reject a newer generation.
- Unpublished immutable key revisions may remain after a failed config write. They are not active credentials and have no automatic garbage collection. Provider deletion and credential cleanup remain separate operations.
- Dispatch requires the published disk config, selected key reference, and credential generation to agree. Legacy generation-1 keys and valid UUID references remain readable. The API cannot roll credentials back by assigning an arbitrary old reference; replacement publishes through the write-only credential field.
- Enable/disable: the Enabled toggle in the detail head or the rail row PUTs `enabled`. Per-model toggles edit `disabledModels`. Detail also has Disable all and Enable all buttons (`aria-label` "Disable all models" / "Enable all models") and an `N off` count.
- Editor (`ProviderModal`, dialog `New provider` or `Edit provider <id>`) has exactly four fields: ID (`#prov-id`, disabled when editing), Wire (segmented buttons in `role=group` "Provider wire"; hidden `select#prov-wire`; editing an unknown wire shows a badge instead), Base URL (`#prov-base`), API key (`#prov-cred`, password, placeholder `paste key`). When editing, Base URL and API key both hint `Empty keeps the current value.`. Footer buttons are Cancel and `Create provider` / `Save changes`. Create is disabled while ID is blank, or while the wire is `responses`, `messages`, or `chat` and Base URL is blank (unless the existing provider already has one). Save re-sends the existing models, disabledModels, syncedModels, pool, and modelSettings unchanged. Default models, Models, Disabled models, credential reference, and the Enabled toggle are not in the editor.
- Adding models: the detail has an add line (`.prov-addinput`, placeholder `model id, e.g. gpt-5.3`) with Add and Configure (opens the model settings modal for a new id). Both disable when the id is blank or already listed.
- Cline catalog: a `free`/`pass` segmented control (`role=group` "Cline catalog") appears in the detail only for cline providers.
- Search: `#provider-search` (placeholder `Filter by id, wire, or model`) filters the rail by provider id, wire, or model.
- Model mode switch (antigravity only): `PUT /api/v1/providers/{id}/model-mode?expectedGeneration=N` with `{"mode":"raw"|"logical"}` moves the stored `models` list between collapsed family ids and raw wire ids in one generation write. Disabled entries and per-model settings travel with their model (family state fans out onto members in raw mode, folds back onto the family id in logical mode). The catalog, routes, and agent integrations observe the new list on the next read; nothing else to re-apply. Non-antigravity wires get 400 `invalid_value`.
- Sync: `POST /api/v1/providers/{id}/sync-models` replaces previously listed models with discovery results and preserves manual models absent from the previous `syncedModels`. Antigravity catalogs belong to one provider and endpoint, persist across restart, and drive both logical and raw routing. Removed models lose associated settings and exact references; retained models keep their settings. Failed discovery writes nothing.
- Existing models with no recorded sync provenance are preserved. Older syncs discarded provenance for models that had already disappeared, so those entries cannot be distinguished from manual additions and require manual removal.
- Catalog fallback: when a model has no manual window and no listing window, the daemon fills the window from a public catalog cache. Each catalog provider casts one vote. A provider that disagrees with itself abstains. The value with the most votes wins when that count is unique. A tie stays unknown. Listing facts and manual settings still win, including a listing that said the model is text-only. Image uses the same vote. The modal caption uses `resolvedFacts.contextSource` (`listing`, `catalog`, or `global`) and does not recompute that chain.
- Catalog efforts: the catalog effort ladder is voted the same way, as one ordered set per provider. Rungs are normalized to `minimal, low, medium, high, xhigh, max` (catalog `none` and unknown rungs drop). A manual `reasoningEfforts` list wins. Otherwise the winning catalog ladder feeds client configs. `resolvedFacts.efforts` and `resolvedFacts.effortsSource` (`manual`, `catalog`, `none`) show the result.
- The Providers detail renders the stored list in both modes with the same rows: toggle, edit, remove buttons, fresh badge, and effort rungs. Rungs render only for models with manually overridden `modelSettings.<model>.reasoningEfforts`; a model without an override shows no rung row. The `manual` badge marks ids the last sync did not report; an absent `syncedModels` list marks none.
- Per-model API: `modelSettings.<model>.wire` (`responses`, `chat`, or `messages`) overrides the provider wire for that model only. The provider keeps one base URL and one credential. Only providers whose own wire is `responses`, `chat`, or `messages` accept it; codex, antigravity, and cline providers reject it with 400. `resolvedFacts.<model>.wire` and `wireSource` (`model` or `provider`) report the effective value. The model settings modal shows an "API" section for those providers, and the model row carries a wire tag when the override is set. Picking the provider's own wire in the modal stores no override.

## How to get to it (user POV)

Click `Providers` (`#/providers`). `New provider` opens the four-field editor. Click a rail row to select a provider; its detail pane has the Enabled toggle, model list with per-model toggles, Sync (`Refresh models from provider`), Disable all / Enable all, the add-model line, Edit, and Delete. Antigravity providers with families also show a `raw models` / `logical models` button.

## Driving it with the harness

```sh
bash verify/scripts/desktop-controls.sh
bash verify/scripts/prismctl-proof.sh
bash verify/scripts/api-sweep.sh
bash verify/scripts/model-mode-proof.sh
```

`desktop-controls.sh` proves the current UI path for create, per-model toggle, and delete of a throwaway `ui-probe` provider through real clicks. It does not currently prove Edit, the provider enabled toggle, credentials, cline catalog, or search. `prismctl-proof.sh` exercises the CLI mutation ladder; `api-sweep.sh` proves POST/PUT/DELETE and a stale-generation 409; `model-mode-proof.sh` proves the logical↔raw switch round trip through the real UI button, the stored config, and the catalog.

Every mutation uses throwaway provider ids (`ui-probe`, `openai-proxy`), never the user's `codex`/`antigravity`, and cleans up in the same run. Never write credentials for the user's real providers during verification.

## Gotchas

- Generation CAS: every write needs the current `expectedGeneration`; a stale value returns 409 `stale_generation`, not a silent overwrite. The UI surfaces a re-fetch button on that error.
- Deleting a provider never fails on references. It succeeds with 200 and cascades to routes, aliases, and combos (see Delete). A proof that seeds a route or combo pointing at the provider must assert those entries are gone afterward.
- The credential field is write-only: asserting it "round-trips" is wrong by design; assert `credential.state === 'set'` after a write and `unset` after delete through the API. The UI does not render the state anywhere, so a proof must assert its absence in the DOM (no `.prov-prow-sub .badge`, no credential badge in `.prov-detail`, no `Already set` hint in the editor) rather than a rendered value.
- Editing an existing provider from the UI keeps its model list, since the editor has no Models field. Change models through the detail pane (toggle, add, remove, Sync) or the API.
- `responses`/`chat` wires require a Base URL in config validation, doctor, and the API (400 `invalid_value`). The editor also blocks `messages` without a Base URL, which the API allows.
- The wire picker labels differ from wire ids (`chat` is `openai-completions`, `messages` is `anthropic`). Drive it by the label text or the hidden `select#prov-wire` value.
