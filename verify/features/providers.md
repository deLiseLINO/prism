# Providers

The Providers view is the CRUD surface for daemon providers: wires, endpoints, models, credentials, enablement, and pool policy. Every write goes through the generation CAS.

## Sub-features

- List: id, wire, enabled, models, Default model, credential state, base URL. The generation stays in component state for CAS writes and is not rendered.
- Create: `POST /api/v1/providers` with `{id, wire, ...}`; wires are `codex`, `antigravity`, `responses`, `messages`, `chat`.
- Replace: `PUT /api/v1/providers/{id}` with `expectedGeneration`; absent fields preserve existing values (absent=preserve merge).
- Delete: `DELETE /api/v1/providers/{id}?expectedGeneration=N`; also deletes the stored credential.
- Credentials: paste-once write (`credential` in the write body, or `apiKeyRef`); reads show only masked state (`set`/`unset`) — the value never round-trips.
- Enable/disable: a single toggle that PUTs `enabled` (and per-model toggles that edit `disabledModels`).
- Editor form fields: ID, Wire, Base URL, Default model, Models (comma-separated), Disabled models, Credential (password), Credential reference, Enabled toggle.
- Search filters cards by provider id, wire, or model.

## How to get to it (user POV)

Click `Providers` (`#/providers`). `New provider` opens the editor; each provider card has Edit and Delete buttons, an enabled toggle, and per-model toggles.

## Driving it with the harness

```sh
bash verify/scripts/desktop-controls.sh
bash verify/scripts/prismctl-proof.sh
bash verify/scripts/api-sweep.sh
```

`desktop-controls.sh` proves the current UI path for create, per-model toggle, and delete of a throwaway `ui-probe` provider through real clicks. It does not currently prove Edit, the provider enabled toggle, credentials, or search. `prismctl-proof.sh` exercises the CLI mutation ladder; `api-sweep.sh` proves POST/PUT/DELETE and a stale-generation 409.

Every mutation uses throwaway provider ids (`ui-probe`, `openai-proxy`), never the user's `codex`/`antigravity`, and cleans up in the same run. Never write credentials for the user's real providers during verification.

## Gotchas

- Generation CAS: every write needs the current `expectedGeneration`; a stale value returns 409 `stale_generation`, not a silent overwrite. The UI surfaces a re-fetch button on that error.
- A provider whose route still references it cannot be deleted (400 `invalid_document`) — remove the route first.
- The credential field is write-only: asserting it "round-trips" is wrong by design; assert `credential.state === 'set'` after a write and `unset` after delete.
- The editor's Models field is comma-separated text; `splitList` trims and drops empties, so a trailing comma adds nothing. Do not assert an empty-string model failure.
- `responses`/`chat` wires require a Base URL; the editor hint also names `messages`, but config validation and doctor flag only `responses`/`chat`.
