# Overview

Overview is the default renderer route and the user's operational summary. The daemon card leads the grid with live state and endpoint, followed by the vision sidecar picker and the default context window control. It does not replace the detailed management views.

## Sub-features

- Default routing: an empty or unknown hash resolves to `#/overview`. The removed `#/daemon` hash also resolves to `#/overview`.
- Daemon card: full-width leading card with a state chip (ok/warn/danger tone, pulsing dot when operational), and `state`/`endpoint` rows. Read-only; lifecycle is supervised, not manual.
- Vision sidecar: enable/disable toggle plus an eligible-model list (providers with image input enabled); picking a model sets it as the sidecar target.
- Default context window: preset buttons (32k/128k/256k/400k/1M) plus a custom token entry, applied to every model without its own override.
- Partial failure: a failed request renders an error banner rather than inventing values. While daemon data is missing, the endpoint shows `unknown`, not `not listening` (which is reserved for the supervisor actually reporting no endpoint).

## How to get to it (user POV)

Launch Prism, click `Overview`, or navigate to an unknown hash. The daemon card shows the operational chip and endpoint; the two config cards follow.

## Driving it with the harness

```sh
bash scripts/verify/scripts/desktop-controls.sh
```

The harness navigates to an unknown hash and requires Overview, proving the fallback route. It also requires the daemon card's state row to read ready/operational and the endpoint row to equal `http://127.0.0.1:$PORT`, matched against a direct health request. The sidecar and context-window writes are management calls; drive them through the real card controls or compare against `GET /api/v1/providers` (vision sidecar target, per-model settings) after a write.

## Gotchas

- Overview is a summary plus two config writes (sidecar, context window); drive all other writes in the detailed views.
- The daemon card's chip label is `operational` when the supervisor state is `ready` — assert on either string, not on a single one.
- The always-visible sidebar daemon pill is covered by `renderer-status.md`; do not count it as an Overview-only status element.
- The old `#/daemon` bookmark resolves to Overview, not an error. This is the fallback contract, not a redirect feature.
