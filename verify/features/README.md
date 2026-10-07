# prism desktop feature map

Each file lists the user-visible behavior, where to reach it, a verification recipe, and the limits of that recipe. Source descriptions are not evidence of a successful live run.

- [Daemon lifecycle](daemon-lifecycle.md) covers registered service startup, health recovery, app supervision, and explicit service stop.
- [Overview](overview.md) covers the daemon card and default context window.
- [Renderer status](renderer-status.md) covers the status subscription, sidebar, boot gate, and unknown-route fallback.
- [Tray](tray.md) covers native Show and Quit actions and window residency. Headless CDP does not prove those actions.
- [Auth flows](auth-flows.md) covers start, pending, cancellation, and start failures in the Add account dialog.
- [Accounts and quota](accounts-quota.md) covers account cards, remaining quota, unavailable state, and read-only comparison.
- [Providers](providers.md) covers provider edits, credentials, model selection, and generation checks.
- [Combos and routes](combos-routes.md) covers API aliases and resolution. There is no desktop view.
- [Usage](usage.md) covers the Accounts grid at `#/usage`.
- [Integrations](integrations-apply.md) covers Apply, Rollback, and Auto-apply for each supported client.
- [Host switching](hosts-switching.md) covers Machines, remote management, and returning to the local daemon.
- [Client installation](agents-install.md) covers binary status, install, update, and job state.
- [Client compatibility](client-compatibility.md) covers real requests using generated client configs. Repeated-request stress does not prove an uninterrupted session.
- [Management CLI](prismctl.md) covers the `prism` command families.
- [Daemon API](daemon-api.md) covers management and inference routes and access rules.
- [Management views](management-views.md) covers the renderer-to-daemon bridge.
- [Vision](vision.md) covers image input and the sidecar.

The renderer routes are `#/overview`, `#/providers`, `#/usage`, `#/stats`, `#/logs`, `#/integrations`, `#/machines`, and `#/experimental`. The integration driver visits Stats and Logs but only checks that each view opens with content. That is not a filter, paging, or live-update proof. Experimental flags are exercised by the install and integration drivers; not every flag has a full behavioral recipe.

Keep missing coverage explicit. Native tray actions, actual package-manager mutation, all-client inference, and uninterrupted long sessions need their own real runs. A source audit or one passing client does not cover those gaps.

The local quality recipe runs the complete supported desktop suite. Transcript verification reads raw events and checks terminal status, tool execution, counts, and complete evidence manifests; passing metadata alone is not a receipt. Unavailable executables and platform prerequisites stay unavailable, failed clients stay failed, and existing evidence directories are not overwritten. Runtime proofs stop only their own processes. Raw live captures stay private; failed sandboxes remain retained when needed for diagnosis.
