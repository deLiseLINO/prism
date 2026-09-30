# Combos and routes

Combos are named target lists. Routes map an inbound model name to a combo id or a `provider/model` pair. Both are config maps written under the generation CAS. The desktop app has no view for them. The `#/combos` hash falls back to overview. Access is through the management API, `prismctl` and `/v1/models`.

## Sub-features

- Combos API: `GET /api/v1/combos` lists, `PUT /api/v1/combos/{id}` upserts, `DELETE /api/v1/combos/{id}` removes. A combo has `targets` (`provider`, `model`, `weight`), `strategy` (`failover` or `round_robin`), `stickyLimit`, `alias`, `nativeAlias`, `displayName` and `imageInput`.
- Routes API: `GET /api/v1/routes` lists, `PUT /api/v1/routes/{key}` sets `value`, `DELETE /api/v1/routes/{key}` removes.
- CAS. PUT takes `expectedGeneration` in the JSON body. DELETE takes `?expectedGeneration=N` and answers 400 `invalid_generation` without it. A stale value is 409 `stale_generation`. Deleting a missing id or key is 404 `not_found`. A single-item GET is 405.
- Validation runs on every write and answers 400 `invalid_document`. A combo needs a known strategy, a non-negative `stickyLimit`, at least one target, known providers and models, and non-negative weights. A route value must be a combo id or a `provider/model` with a known provider and model. A route key beginning with the claude alias prefix must be a well-formed `claude-<provider>--<model>`.
- Resolution order in the planner is Routes, Aliases, Combos, `claude-<provider>--<model>`, then plain `provider/model`. A route or alias value resolves to a combo first, then to `provider/model`. Disabled targets are skipped when a combo plan is built.
- `strategy`, `weight` and `stickyLimit` are stored and validated but the planner does not use them. A combo plan is its enabled targets in stored order. Do not assert round-robin, weighted or sticky behavior from live traffic.
- `GET /v1/models` lists route keys, alias keys and combo ids whose targets are not blocked, plus one `claude-<provider>--<model>` entry per enabled provider model. The list is sorted and each entry has `object: model`.
- prismctl. `combos list`, `combos set <id> --strategy <failover|round_robin> --target provider/model[:weight] [--target ...] [--sticky-limit N] [--alias A]`, `combos remove <id>`, `routes list`, `routes set <key> <value>`, `routes remove <key>`. `--strategy` and at least one `--target` are required on `combos set`. Bad flags exit 2.

## How to get to it (user POV)

There is no UI. A user edits combos and routes with `prismctl`, or calls the management API with curl. Clients see routed names by listing `/v1/models` or by sending the name as the model.

## Driving it with the harness

```sh
bash verify/scripts/api-sweep.sh
bash verify/scripts/prismctl-proof.sh
```

`api-sweep.sh` PUTs a throwaway combo `probe-combo`, PUTs route `probe-key` to `codex/gpt-5.6-luna`, reads `/api/v1/routes` back, then deletes the route and the combo, each under the current generation. Its only 409 check is on providers, so the combo and route ladder does not prove stale-generation conflicts. `prismctl-proof.sh` runs `combos list` and `routes list`, then `combos set mirror`, list, `combos remove`, `routes set default`, list, `routes remove`, and checks that a bad `--strategy` exits 2.

For a manual run, use curl with the generation in every write.

```sh
GEN=$(curl -s $BASE/api/v1/combos | jq .generation)
GEN=$(curl -s -X PUT $BASE/api/v1/combos/verify-combo -H 'Content-Type: application/json' \
  -d "{\"strategy\":\"failover\",\"targets\":[{\"provider\":\"codex\",\"model\":\"gpt-5.6-luna\",\"weight\":1}],\"expectedGeneration\":$GEN}" | jq .generation)
curl -s -X PUT $BASE/api/v1/routes/verify-alias -H 'Content-Type: application/json' \
  -d "{\"value\":\"verify-combo\",\"expectedGeneration\":$GEN}"
curl -s $BASE/v1/models | jq '.data[].id' | grep verify-alias
```

Delete the route first, then the combo. Deleting a combo that a route still names fails validation with a 400, since the route value no longer resolves. Clean up both in the same run.

## Gotchas

- A bare model name as a route value is a 400. Use a combo id or `provider/model`.
- Combo PUT is a full replace. A missing `targets` or an empty list is a 400, and omitted optional fields reset to defaults, including `imageInput` to false.
- Weight 0 is legal. It has no effect on the planner today, so do not "fix" it.
- `stickyLimit` 0 is legal and also has no effect on the planner today.
- Two writes on the same generation race the CAS and the second returns `stale_generation`. Re-read the generation between writes.
- `styles.css` still has orphan `.combo*` rules. They belong to no view.
