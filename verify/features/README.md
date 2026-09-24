# Prism desktop feature map

User-facing features of the Prism system (the Electron desktop app in `apps/desktop`, the `prismd` daemon, the `prismctl` CLI, and the Codex/Grok/OMP integration clients), from the operator's point of view. Each file answers, in this exact order: what the feature's sub-features are, how to reach it as a user, how to drive it with the harness from `SKILL.md`, and what gotchas bite the verification driver.

- `daemon-lifecycle.md` — the app supervises prismd: boot, crash restart, quit; window close hides to the tray instead of stopping anything. The Overview card reports state and endpoint; there are no manual controls.
- `overview.md` — the default dashboard: daemon card (state, endpoint), vision sidecar behind the experimental flag, default context window.
- `renderer-status.md` — the window shows live daemon status, subscribes to supervisor pushes, and the tray tooltip mirrors it.
- `tray.md` — tray icon, Show Prism, Quit Prism, click-to-show, live tooltip.
- `auth-flows.md` — per-provider OAuth add-account: start login, pending device state, browser handoff, cancel, terminal states.
- `accounts-quota.md` — account rows, quota bars and the precise `no quota` state, pause/resume/priority/remove, selection policy, and the read-only quota proof.
- `providers.md` — provider CRUD with generation CAS, credential paste-once, model enable/disable; the per-provider model list and sync live on the provider card (the old read-only Models tab was removed with the split-detail providers view).
- `combos-routes.md` — combo create/edit/delete and route alias mapping under CAS.
- `usage.md` — the real per-account usage view backed by the pool snapshot.
- `integrations-apply.md` covers applying and rolling back client configs for codex, grok, omp, claude, pi, opencode, and hermes.
- `hosts-switching.md` — the Integrations host selector: local and remote hosts, switching reloads statuses, apply/rollback act on the selected host, honest unresolved statuses.
- `agents-install.md` — install and update the agent binaries behind the integrations through the `/api/v1/agents*` routes and `prismctl agents`.

- `client-compatibility.md` — UI-applied Grok and OMP configurations complete real requests, OMP exposes thinking, and the live matrix holds Luna, Gemini 3.7 Flash, and GLM 5.3 sessions alive for >=300 seconds each.
- `prismctl.md` — the full prismctl command family against the daemon management API.
- `daemon-api.md` — every management and inference route prismd serves, including the loopback-or-bearer admit rule.
- `management-views.md` — how the renderer's management bridge funnels every view to the daemon API, and what the bridge refuses.

Coverage rule: every renderer route (`#/overview`, `#/providers`, `#/usage`, `#/stats`, `#/logs`, `#/integrations`, `#/machines`, `#/experimental`), every tray action, every prismctl command family, every inference endpoint, and every integration client must have a recipe in exactly one file above. Keep this map honest as the app changes: after any user-facing change, update the matching file in the same change. Re-run the full map periodically with `/maintain-verification-skill`. Known gaps as of the daemon-tab removal: `#/stats`, `#/logs`, and `#/experimental` have no dedicated recipe file yet (`#/experimental` is exercised incidentally by the remote-install proof's flag toggle); `#/machines` is covered by `hosts-switching.md`.
