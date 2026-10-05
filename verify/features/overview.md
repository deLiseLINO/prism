# Overview

Overview is the default renderer route and the user's operational summary. The daemon card leads the grid with live state and endpoint, followed by the default context window control. The vision sidecar card lives here too, but only behind the experimental `visionSidecar` flag. It does not replace the detailed management views.

## Sub-features

- Default routing: an unknown hash resolves to `#/overview`. The removed `#/daemon` hash also resolves to `#/overview`. An empty startup hash normalizes to Overview, even if the stored view differs.
- Daemon card: state chip (ok/warn/danger tone, pulsing dot when operational) and `state`/`endpoint` rows, one grid cell like the other cards. Read-only; lifecycle is supervised, not manual.
- Vision sidecar (experimental): hidden unless the `visionSidecar` flag is on. The card has an `Enabled`/`Disabled` toggle and a model select. The select lists `provider/model` ids whose provider is enabled, whose model is not disabled, and whose resolved image input is true. Picking a model sets it as the target and enables the sidecar. With no eligible model the card shows an empty state. A configured target that is no longer eligible shows a warning banner. The flag itself lives on `#/experimental`.
- Default context window: preset buttons (32k/128k/256k/400k/1M) plus a custom token entry, applied to every model without its own override.
- Partial failure: a failed request renders an error banner rather than inventing values. While daemon data is missing, the endpoint shows `unknown`, not `not listening` (which is reserved for the supervisor actually reporting no endpoint).

## How to get to it (user POV)

Launch Prism, click `Overview`, or navigate to an unknown hash. The daemon card shows the operational chip and endpoint; the context window card follows. The vision sidecar card appears only after turning on its flag on the Experimental screen.

## Driving it with the harness

```sh
bash verify/scripts/desktop-controls.sh
```

The harness navigates to an unknown hash and requires Overview, proving the fallback route. It also requires the daemon card's state row to read ready/operational and the endpoint row to equal `http://127.0.0.1:$PORT`. A direct `GET /api/v1/health` must also succeed, but its body is not compared to the card. The context-window card is not driven by any script. The legacy `vision-sidecar-proof.sh` is not migrated to checked process and CDP ownership. Do not count it as an owned-runtime proof. See `vision.md` for the remaining coverage.

## Gotchas

- The sidecar card is invisible until the `visionSidecar` experimental flag is on; `vision-sidecar-proof.sh` toggles it from the Experimental screen before driving the card. With the flag off, the card must not render at all.
- Overview is a summary plus the context-window config write; drive all other writes in the detailed views.
- The daemon card's chip label is `operational` when the supervisor state is `ready`, so assert on either string. Other states show their own name (`failed`, `idle`, `stopped`, `quitting`), plus `unreachable` and `checking…` before status arrives.
- The always-visible sidebar daemon pill is covered by `renderer-status.md`; do not count it as an Overview-only status element.
- The old `#/daemon` bookmark resolves to Overview, not an error. This is the fallback contract, not a redirect feature.
