# Daemon API

`prismd` serves two route families on one port: `/api/v1/*` management (loopback exempt, remote management compares the single bearer value with the configured management token; when it is empty, any non-empty bearer is accepted) and `/v1/*` inference (admitted only from loopback or with any `Authorization: Bearer` token). This file is the inventory contract: every supported endpoint must appear here with its method, body, response, and error shapes.

## Sub-features

Management family (`internal/management/server.go`):

- `GET /api/v1/health` — `{status:"ok"}`.
- `GET /api/v1/models` — provider models from enabled providers (`<provider>/<model>` with caps); combos/routes/aliases live on `GET /v1/models`, not here.
- `GET /api/v1/providers`, `POST /api/v1/providers`, `PUT/DELETE /api/v1/providers/{id}` — provider CRUD under generation CAS; credential write-only. There is no single-item provider GET.
- `GET /api/v1/accounts`, `DELETE /api/v1/accounts/{id}` — list and remove.
- `POST /api/v1/accounts/{id}/pause|resume|priority` — version CAS mutations.
- `GET /api/v1/accounts/{id}/quota` — one account's quota snapshot.
- `GET /api/v1/combos`, `PUT/DELETE /api/v1/combos/{id}` — combo list plus item mutations under generation CAS. There is no single-item combo GET.
- `GET /api/v1/routes`, `PUT/DELETE /api/v1/routes/{key}` — alias list plus item mutations under generation CAS. There is no single-item route GET.
- `GET /api/v1/usage` — pool-wide per-account quota snapshot.
- `POST /api/v1/auth/{provider}/start|callback`, `GET /api/v1/auth/{provider}/status[?session=]` — OAuth flows (codex, antigravity).
- `GET /api/v1/integrations[/{client}]`, `POST /api/v1/integrations/{client}/apply|rollback` — client config management.
- `GET /api/v1/agents[/{id}]`, `POST /api/v1/agents/{id}/install[?force=true]|update`, `GET /api/v1/agents/{id}/job` — agent binary install/update jobs (202 envelope `{job}`; 409 `install_active`; 400 `not_installed`; an environment with no usable plan answers 200 with state `unsupported`).
- Fallback: unknown path under `/api/v1/` is 404 `not_found`; known path with wrong method is 405 `method_not_allowed`; malformed JSON body is 400 `malformed_json`; errors are `{error:{code, message}}`.

Inference family (`internal/server/server.go`):

- `POST /v1/responses` — OpenAI Responses; SSE out; codex CLI (originator `codex`), grok CLI (`x-prism-grok: 1`), and generic clients.
- `POST /v1/chat/completions` — OpenAI Chat; streaming deltas with `reasoning_content`, or a single aggregate; always classified as the OMP client.
- `POST /v1/messages` (+ `POST /v1/messages/count_tokens`) — Anthropic Messages for Claude-style clients via `claude-<provider>--<model>` aliases.
- `POST /v1/responses/compact` — compaction; JSON out; falls back to a local summary.
- `GET /v1/models` — routes, aliases, and combos minus blocked, plus one `claude-<provider>--<model>` entry per enabled provider/model pair, in OpenAI list shape (`type`, `id`, `display_name`). Claude Code's gateway model discovery populates its model picker from this list.

Admission: `admit` lets loopback through untouched; remote callers need a bearer token everywhere and the management token for `/api/v1/*`. Verification runs loopback and needs no token.

## How to get to it (user POV)

Users reach the daemon through the desktop UI, prismctl, or a configured client (Codex, Grok, OMP, or any OpenAI/Anthropic-compatible tool pointed at the daemon's inference base URL: `http://127.0.0.1:8787/v1` for a standalone prismd on its default port, `http://127.0.0.1:18787/v1` for the desktop-supervised daemon the verification skill drives). The API itself is the integration surface third parties code against.

## Driving it with the harness

```sh
bash scripts/verify/scripts/api-sweep.sh
```

`api-sweep.sh` is a deterministic smoke sweep, not complete live inference coverage. It boots an isolated daemon (port 18792, throwaway config) and asserts in one run:

- every management GET answers 200 (`health`, `models`, `providers`, `accounts`, `combos`, `routes`, `usage`, `integrations`, `integrations/{grok,omp}`);
- the negative surface: unknown path 404, wrong method 405, malformed JSON 400, stale CAS write 409 (`stale_generation`);
- mutation ladder with read-back: provider create/update/delete, combo set/delete, route set/delete, all under `expectedGeneration` CAS;
- auth `start` returns `{session,url}` and `status?session=` stays `pending` — cancelled-login contract, no browser, no account;
- inference shapes: `GET /v1/models` 200, `POST /v1/messages/count_tokens` with a `claude-` alias returns `input_tokens > 0`, no-route models 404 on both count_tokens and compact, malformed `/v1/responses` body 400, wrong method 405, unknown `/v1/*` path 404.

Messages-family models must be `claude-<provider>--<model>` aliases; plain `provider/model` names are rejected by the messages ingress with 400. The alias does not need a configured route: the planner derives `<provider>/<model>` from any `claude-` alias of an enabled provider, so `claude -p --model claude-codex--gpt-5.6-luna` works with no user routes.

## Gotchas

- Loopback-or-bearer: from a non-loopback address without a bearer header, every `/v1/*` call 401s (`missing bearer token`). Verification always runs loopback, so it never needs a token — a token-asserting probe must bind a non-loopback source deliberately.
- The management API admits loopback freely and demands the management token from remote callers: never expose the port beyond loopback, and never write a probe that assumes no auth header works remotely. Verification runs loopback.
- A 5-second stall watchdog cancels an inference stream with no frames: a "hang" test will terminate, not block forever.
- Error envelopes differ per family — management `{error:{code,message}}`, chat `{error:{message,type,code}}`, responses SSE `response.failed`, messages an `error` event. Match the family, not a generic shape.
- `previous_response_id` is accepted but warned unmapped on `/v1/responses`; conversation continuity is the client's responsibility, not the daemon's.
