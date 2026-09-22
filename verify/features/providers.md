# Providers

The Providers view is the CRUD surface for daemon providers: wires, endpoints, models, credentials, enablement, and pool policy. Every write goes through the generation CAS.

## Sub-features

- List: id, wire, enabled, models, Default model, base URL. The generation stays in component state for CAS writes and is not rendered. Credential state is deliberately not rendered anywhere in the providers UI (list, detail, editor).
- Create: `POST /api/v1/providers` with `{id, wire, ...}`; wires are `codex`, `antigravity`, `responses`, `messages`, `chat`.
- Replace: `PUT /api/v1/providers/{id}` with `expectedGeneration`; absent fields preserve existing values (absent=preserve merge).
- Delete: `DELETE /api/v1/providers/{id}?expectedGeneration=N`; also deletes the stored credential.
- Credentials: paste-once write (`credential` in the write body, or `apiKeyRef`); reads show only masked state (`set`/`unset`) in the API; the value never round-trips and the UI never renders the state.
- Enable/disable: a single toggle that PUTs `enabled` (and per-model toggles that edit `disabledModels`).
- Editor form fields: ID, Wire, Base URL, Default model, Models (comma-separated), Disabled models, Credential (password), Credential reference, Enabled toggle. The credential field always shows the placeholder `paste key`; for an existing provider its hint reads `Empty keeps the current value.`
- Search filters cards by provider id, wire, or model.
- Model mode switch (antigravity only): `PUT /api/v1/providers/{id}/model-mode?expectedGeneration=N` with `{"mode":"raw"|"logical"}` moves the stored `models` list between collapsed family ids and raw wire ids in one generation write. Disabled entries and per-model settings travel with their model (family state fans out onto members in raw mode, folds back onto the family id in logical mode). The catalog, routes, and agent integrations observe the new list on the next read; nothing else to re-apply. Non-antigravity wires get 400 `invalid_value`.
- Sync: `POST /api/v1/providers/{id}/sync-models` merges remote discovery into the stored list, folding raw family members onto their logical id; a raw-mode provider re-expands after the fold. Denied service ids and `isInternal` entries never enter the list, and the merge drops them from stored lists.
- The Providers detail renders the stored list in both modes with the same rows: toggle, edit, remove buttons, fresh badge, and effort rungs (logical families only). The `manual` badge marks ids the last sync did not report; an absent `syncedModels` list marks none.

## How to get to it (user POV)

Click `Providers` (`#/providers`). `New provider` opens the editor; each provider card has Edit and Delete buttons, an enabled toggle, and per-model toggles.

## Driving it with the harness

```sh
bash verify/scripts/desktop-controls.sh
bash verify/scripts/prismctl-proof.sh
bash verify/scripts/api-sweep.sh
bash verify/scripts/model-mode-proof.sh
```

`desktop-controls.sh` proves the current UI path for create, per-model toggle, and delete of a throwaway `ui-probe` provider through real clicks. It does not currently prove Edit, the provider enabled toggle, credentials, or search. `prismctl-proof.sh` exercises the CLI mutation ladder; `api-sweep.sh` proves POST/PUT/DELETE and a stale-generation 409; `model-mode-proof.sh` proves the logical↔raw switch round trip through the real UI button, the stored config, and the catalog.

Every mutation uses throwaway provider ids (`ui-probe`, `openai-proxy`), never the user's `codex`/`antigravity`, and cleans up in the same run. Never write credentials for the user's real providers during verification.

## Gotchas

- Generation CAS: every write needs the current `expectedGeneration`; a stale value returns 409 `stale_generation`, not a silent overwrite. The UI surfaces a re-fetch button on that error.
- A provider whose route still references it cannot be deleted (400 `invalid_document`) — remove the route first.
- The credential field is write-only: asserting it "round-trips" is wrong by design; assert `credential.state === 'set'` after a write and `unset` after delete through the API. The UI does not render the state anywhere, so a proof must assert its absence in the DOM (no `.prov-prow-sub .badge`, no credential badge in `.prov-detail`, no `Already set` hint in the editor) rather than a rendered value.
- The editor's Models field is comma-separated text; `splitList` trims and drops empties, so a trailing comma adds nothing. Do not assert an empty-string model failure.
- `responses`/`chat` wires require a Base URL; the editor hint also names `messages`, but config validation and doctor flag only `responses`/`chat`.
