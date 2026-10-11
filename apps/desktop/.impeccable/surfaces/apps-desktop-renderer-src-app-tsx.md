---
version: 1
slug: "apps-desktop-renderer-src-app-tsx"
primary_target: "apps/desktop/renderer/src/App.tsx"
related_targets: []
---

# Surface brief: desktop app shell (redesign prototype)

Mode: Operate. Audience: owner (developer) plus friends who are not developers. Job: see if routing works, how much quota is left, add an account, connect a tool, read what failed. Prototype only: static, mock data, every screen and action live.

## Direction contract

THESIS: prism is a prism. One beam of requests from the user's apps enters the glass and leaves as separate coloured rays, one per account or provider. The refused default: a grey admin dashboard of cards with a blue accent.
OWN-WORLD: deep indigo rail in both themes, chalk ground in light and ink-violet ground in dark, one violet-indigo action colour. Seven spectrum hues give each provider an identity dot; state (ok, low, out, needs sign-in) is carried by green, amber, red with words and icons. One humanist sans built for legibility, numerals in its mono.
STORY: the visitor understands in one glance whether everything works, which account is in use, and what needs a click. Plain words first; technical names (wire, endpoint, request id) appear only when "Technical details" is on.
FIRST VIEWPORT: Home. Left rail (Everyday / Setup / Advanced). Top: one status sentence with its primary fix action. Below: the Beam (apps on the left, prism in the middle, rays to accounts on the right, ray width is request share, failover shown as a bent dashed ray). Right column: needs-attention list and quick settings (context window, image helper).
FORM: operate shell with persistent rail, 3rd candidate on the ordered list, seed 7fcbf2c3.
FINISH: unreviewed and undocumented is unfinished; this build ends with the finish review, the verdict, DESIGN.md, and every shipping raster carrying its provenance
