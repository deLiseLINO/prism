# Daemon API

`prismd` serves two route families on one port: `/api/v1/*` management (loopback exempt, remote management compares the single bearer value with the configured management token; when it is empty, any non-empty bearer is accepted) and `/v1/*` inference (admitted only from loopback or with any `Authorization: Bearer` token). This file is the inventory contract: every supported endpoint must appear here with its method, body, response, and error shapes.

## Sub-features

Management family (`internal/management/server.go`):

- `GET /api/v1/health` — `{status:"ok"}`.
- `GET /api/v1/models` — provider models from enabled providers (`<provider>/<model>` with caps); combos/routes/aliases live on `GET /v1/models`, not here.
- `GET /api/v1/providers`, `POST /api/v1/providers` (409 `already_exists`), `PUT/DELETE /api/v1/providers/{id}` (PUT 404 `not_found` when absent) — provider CRUD under generation CAS; credential write-only. There is no single-item provider GET.
- `POST /api/v1/providers/{id}/sync-models[?expectedGeneration=]` — refresh the provider's model list from upstream. `PUT /api/v1/providers/{id}/model-mode` — set the provider's model mode.
- `PUT /api/v1/vision-sidecar` — body `{enabled, target, expectedGeneration}`; echoes the stored values with the new generation. `PUT /api/v1/context-window` — body `{contextWindow, expectedGeneration}`; negative values 400 `invalid_value`.
- `GET /api/v1/accounts`, `DELETE /api/v1/accounts/{id}` — list and remove.
- `POST /api/v1/accounts/{id}/pause|resume|priority` — version CAS mutations.
- `GET /api/v1/accounts/{id}/quota` — one account's quota snapshot. `POST /api/v1/accounts/{id}/quota/refresh` — live refresh with a 30 second timeout.
- `GET /api/v1/combos`, `PUT/DELETE /api/v1/combos/{id}` — combo list plus item mutations under generation CAS. There is no single-item combo GET.
- `GET /api/v1/routes`, `PUT/DELETE /api/v1/routes/{key}` — alias list plus item mutations under generation CAS. There is no single-item route GET.
- `GET /api/v1/usage` — pool-wide per-account quota snapshot.
- `GET /api/v1/stats[?range=1h|24h|7d|30d|all]` — aggregate `{range, overview, models, providers}` from the usage store; default `1h`; other range 400 `invalid_range`; no store 503 `stats_unavailable`.
- `GET /api/v1/requests[?limit=N]` — recent request journal, newest first, `{requests, dropped}`; bad limit 400 `invalid_limit`; no journal 503 `not_available`.
- `POST /api/v1/auth/{provider}/start|callback`, `GET /api/v1/auth/{provider}/status[?session=]` — OAuth flows (codex, antigravity, cline). The provider segment resolves to the first configured provider with that wire.
- `GET /api/v1/hosts`, `POST /api/v1/hosts` (body `{id, address, daemonPort?, expectedGeneration}`; id `local` reserved; 409 `already_exists`), `PUT /api/v1/hosts/{host}` (404 `not_found`), `DELETE /api/v1/hosts/{host}[?expectedGeneration=]` — remote host registry under generation CAS. Invalid input is 400 `invalid_document`. The `local` host cannot be replaced or deleted.
- `GET /api/v1/hosts/{host}/integrations[/{client}]`, `POST /api/v1/hosts/{host}/integrations/{client}/apply|rollback` — the same client config management run against a registered host.
- `GET /api/v1/integrations[/{client}]`, `POST /api/v1/integrations/{client}/apply|rollback`, `PUT /api/v1/integrations/{client}/enabled` — client config management and the per-client enabled toggle.
- `GET /api/v1/agents[/{id}]`, `POST /api/v1/agents/{id}/install[?force=true]|update`, `GET /api/v1/agents/{id}/job` — agent binary install/update jobs (202 envelope `{job}`; 409 `install_active`; 400 `not_installed`; an environment with no usable plan answers 200 with state `unsupported`).
- Fallback: unknown path under `/api/v1/` is 404 `not_found`; known path with wrong method is 405 `method_not_allowed`; malformed JSON body is 400 `malformed_json`; stale CAS is 409 `stale_generation`; features the build cannot serve answer 501 `unsupported`; errors are `{error:{code, message}}`.

Inference family (`internal/server/server.go`):

- `POST /v1/responses` supports SSE and buffered JSON. Buffered failures use an HTTP error status and preserve the provider error in the response object. Native completed, incomplete, and failed terminals retain their status.
- `POST /v1/chat/completions` — OpenAI Chat; streaming deltas with `reasoning_content`, or a single aggregate; always classified as the OMP client.
- `POST /v1/messages` (+ `POST /v1/messages/count_tokens`) — Anthropic Messages. Accepts plain `<provider>/<model>` (omp) and `claude-<provider>--<model>` aliases (Claude Code). The decoder requires only `model`, `max_tokens`, and `messages`; it does not check the model form.
- `POST /v1/responses/compact` returns replacement history as `{output:[...]}`. Supported native compactors preserve opaque output unchanged and refuse incomplete results. Other targets run a real tool-free summary turn through normal routing and accounting. Failures retain their HTTP status and valid `Retry-After` guidance without installing a partial summary. A Chat target refuses encrypted compaction history before dispatch because that wire cannot represent the opaque state.
- `GET /v1/models` — routes, aliases, and combos minus blocked, plus one `claude-<provider>--<model>` entry per enabled provider/model pair, in OpenAI list shape (`type:"model"`, `id`, `display_name`, `object`). Claude Code's gateway model discovery populates its model picker from this list.

Model resolution (`internal/server/planner.go`), first match wins:

1. `routes` key. 2. `aliases` key. 3. `combos` key. 4. `claude-<provider>--<model>` alias. 5. plain `<provider>/<model>`.

Route and alias values may name a combo or a `<provider>/<model>`. A `claude-` alias of an unknown or disabled provider does not fall through to the plain form. An unresolved model is 404 `not_found_error` on `count_tokens` and compact; `/v1/messages` fails in the pipeline.

Admission: `admit` lets loopback through untouched; remote callers without a bearer get 401 `unauthorized`, and on `/api/v1/*` a configured management token that does not match gets 403 `forbidden`. Verification runs loopback and needs no token.

## How to get to it (user POV)

Users reach the daemon through the desktop UI, prismctl, or a configured client (OMP, Claude, Codex, Grok, or any OpenAI/Anthropic-compatible tool pointed at the daemon's inference base URL, `http://127.0.0.1:10200/v1` on the default port). The API itself is the integration surface third parties code against.

## Driving it with the harness

```sh
bash verify/scripts/api-sweep.sh
```

`api-sweep.sh` is a deterministic smoke sweep, not complete live inference coverage. It boots an isolated daemon (port 18792, throwaway config) and asserts in one run:

- every management GET answers 200 (`health`, `models`, `providers`, `accounts`, `combos`, `routes`, `usage`, `integrations`, `integrations/{grok,omp}`);
- the negative surface: unknown path 404, wrong method 405, malformed JSON 400, stale CAS write 409 (`stale_generation`);
- mutation ladder with read-back: provider create/update/delete, combo set/delete, route set/delete, all under `expectedGeneration` CAS;
- auth `start` returns `{session,url}` and `status?session=` stays `pending` — cancelled-login contract, no browser, no account;
- inference shapes: `GET /v1/models` 200, `POST /v1/messages/count_tokens` with a `claude-` alias returns `input_tokens > 0`, no-route models 404 on both count_tokens and compact, malformed `/v1/responses` body 400, wrong method 405, unknown `/v1/*` path 404.

Both messages model forms work. `claude -p --model claude-codex--gpt-5.6-luna` needs no user routes, because the planner derives `<provider>/<model>` from any `claude-` alias of an enabled provider. The sweep only exercises the alias path (an explicit alias route for `count_tokens`, and `claude-none--no-such` for the 404). It does not cover plain `provider/model` on `/v1/messages`, the route-less alias, or the newer management endpoints (`stats`, `requests`, `hosts`, `sync-models`, `model-mode`, `vision-sidecar`, `context-window`, `quota/refresh`, `integrations/{client}/enabled`, host integrations). Probe those by hand with curl against a running daemon (default port 10200).

## Gotchas

- Loopback-or-bearer: from a non-loopback address without a bearer header, every `/v1/*` call 401s (`missing bearer token`). Verification always runs loopback, so it never needs a token — a token-asserting probe must bind a non-loopback source deliberately.
- The management API admits loopback freely and demands the management token from remote callers: never expose the port beyond loopback, and never write a probe that assumes no auth header works remotely. Verification runs loopback.
- Streaming first-progress and idle waits are independently configurable; their default is 300 seconds. Cancellation releases the selected account lease. Failure can move to another target only before output or state has been exposed; post-output failure remains the selected attempt's failure.
- Error envelopes differ per family — management `{error:{code,message}}`, chat `{error:{message,type,code}}`, responses SSE `response.failed`, messages an `error` event. Match the family, not a generic shape.
- Requests must carry explicit replay history. Unsupported server-stored continuation controls are rejected rather than silently ignored. Function and response-format schemas and tool replay preserve exact JSON numeric lexemes. Freeform tools lowered to function wires use a required string `input` parameter and return the raw input on custom-tool output and replay.
- Supported tool restrictions, structured output, reasoning effort, text verbosity, sampling, output ordering, and encrypted reasoning state are carried through the relevant wire. Native usage must contain exact nonnegative integral counts with valid sums and cache accounting; malformed observations cannot publish successful usage. Buffered throttle failures retain valid `Retry-After` guidance.
- The supported Antigravity generation wire sends function schemas as exact `parametersJsonSchema`. Alternate model families retain supported legacy parameters and refuse numeric bounds, non-string enums, and other unrepresentable constraints before dispatch. That refusal is not proof that an external model supports those keywords.
- A freeform tool lowered to a function-only wire preserves its raw input string, but the bridge does not enforce grammar syntax. Native custom-tool declarations retain their grammar format. An HTTP delivery check does not prove that a model obeyed the grammar.
- Native targets that cannot represent an explicitly requested verbosity or sampling combination refuse that request before dispatch. Supported no-thinking sampling remains forwarded. The proxy does not silently remove a user-selected output control to make the request succeed.
- Native completed messages preserve refusal text. Completed custom-tool calls require a present string input; an explicit empty string remains valid. Final snapshots and receipts cannot contradict received text or tool-input deltas. Invalid completion produces a failed response in both buffered and streaming modes.
- Custom Responses and Chat usage distinguishes missing fields from explicit null, accepts exact integral decimal and exponent forms, checks overflow and inclusive totals, and retains the last valid snapshot on failure. Streaming errors may retain HTTP 200 after headers are sent; the terminal status still reports failure.
