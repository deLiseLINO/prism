# Grok and OMP client compatibility (live matrix)

Prism-generated configuration must work in the real client, not only parse as TOML or YAML. Grok must complete through the Responses endpoint. OMP must complete without an immediate protocol error and must expose model thinking when the user requests it. The live matrix extends this to the three application models a user selects inside the clients — Luna, Gemini 3.7 Flash, and GLM 5.3 — with one >=300-second session per supported client/model pair.

## Sub-features

- Grok loads the model aliases written by the Integrations screen.
- Grok completes one live request against the first daemon model without retrying. Override with `PRISM_VERIFY_GROK_MODEL`.
- OMP loads the provider and models written by the Integrations screen.
- OMP completes one live request with `--tools read --thinking high --print-thoughts`. Override the model with `PRISM_VERIFY_OMP_MODEL`.
- OMP emits a final answer in structured output. Thinking is optional on the single-model probe (tool turns) and mandatory in the matrix.
- OMP completes a real read-tool call and receives its result before the final answer.
- Client failures preserve stderr and the structured event stream.
- Live matrix: for each of `codex/gpt-5.6-luna`, `antigravity/gemini-3.7-flash`, and one model taken from the daemon's model list at runtime (first entry, chat-wire or messages-wire), Grok and OMP each hold one session alive >=300 seconds with exit 0, a substantive final response, and zero transport errors.
- Mixed wire in OMP: the leaf keeps provider-level `api: openai-completions` and `apiKey: prism-loopback`, writes plain ids `- id: <provider>/<model>`, and adds `api: anthropic-messages` per model only for messages-wire models, plus leaf-level `auth: apiKey`. Messages-wire models reach the daemon on `/v1/messages` with the plain `<provider>/<model>` id, which the ingress accepts. OMP never uses `claude-<provider>--<model>` aliases; only Claude Code does.
- New-agent smoke (`verify/scripts/bigtask-drive.sh`): Claude, Pi, OpenCode, and Hermes each answer one live request through the config written by Integrations. Claude uses the `claude-<provider>--<model>` alias; Pi and Hermes use the plain model id; OpenCode uses `opencode run --standalone --model prism/<id>` in an isolated home. Require exit 0, the marker, and a substantive response (>=2000 chars).

## How to get to it (user POV)

Click Apply for Grok and OMP in Prism. Grok and Codex/Claude rows stay hidden until the `Other agents` experimental flag is on; OMP and OpenCode are always visible. Start each client normally, select its Prism model. For the matrix models Grok uses `prism-codex-gpt-5-6-luna`, `prism-antigravity-gemini-3-7-flash`, and the derived alias (`prism-` plus the id with each character outside `[A-Za-z0-9_-]` replaced by `-`). OMP uses `prism/codex/gpt-5.6-luna`, `prism/antigravity/gemini-3.7-flash`, and `prism/<provider>/<model>` for the derived model. Send a prompt that requests reasoning. The request must finish. OMP must render thinking before the final answer.

## Driving it with the real clients

Single-model probes:

```sh
PRISM_VERIFY_LIVE=1 \
  verify/scripts/integration-drive.sh
```

The helper uses only files created by the UI click. It checks `grok models` lists `prism-` and `omp models` lists a `prism (` line, and checks `models.yml` has `api: openai-completions`, `apiKey: prism-loopback`, plain `- id: <provider>/<model>` lines, a `max` effort level, and no `claude-...--...` id. `api: anthropic-messages` must appear only on messages-wire model entries, with `auth: apiKey` at leaf level. It runs Grok with the generated alias (marker `PRISM_GROK_LIVE_OK`). It asks OMP to read a sandbox file with the real `read` tool in NDJSON mode with `--thinking high --print-thoughts`, then passes the stream to `assert-omp-output.mjs --require-tool --thinking-optional`. That requires the marker (`PRISM_OMP_THINKING_OK`), tool call, and tool result; missing thinking is only a warning.

The full matrix:

```sh
verify/scripts/live-matrix.sh
```

It runs six pairs (2 clients x 3 models), each a sequence of long-generation prompts that must stay alive >=300 seconds total (floor `PRISM_MATRIX_MIN_SECONDS`, ceiling `PRISM_MATRIX_CEILING_SECONDS`), closed by one short marker request once the floor is reached. The derived-model pair uses counting prompts, Grok `--reasoning-effort low`, and OMP `--thinking off`. Codex and antigravity OMP pairs use `--thinking medium`. Only OMP antigravity essays are shorter (4,000-5,000 chars); Grok essays are 6,000-9,000. Failed attempts are retried up to three times and preserved under `pairs/<pair>-failed/`. Each pair records model ID, client, provider, leased account, start/end timestamps, elapsed, exit code, transport error count, marker count, final response length, stdout/NDJSON, stderr, and the daemon log slice (in `<userData>/prismd.log`). `assert-matrix-run.mjs` re-verifies the whole evidence run including the sha256 manifest. Check each OMP `cmd.txt` has `--model prism/<provider>/<model>`. A messages-wire derived model shows `POST /v1/messages` with a plain id and status 200 in its log slice.

A matrix pass requires, per pair:

- elapsed >= 300 seconds and exit 0;
- the unique marker exactly once in the final assistant text of the marker request, with >=1000 total assistant characters across the pair;
- zero transport errors (no `error`/`agent_error` events, no `upstream_transport` envelopes, no failed tool executions; corroborated by no account sliding to `cooling_down`/`soft_avoid` in the usage snapshots);
- daemon health 200 before and after.

## Gotchas

- `Working...` is progress UI, not proof of thinking.
- Reasoning token usage does not prove that OMP received a displayable thinking block.
- The single-model probe does not require thinking; a final answer plus tool call and result satisfies it. Thinking is enforced in the matrix and `bigtask-drive.sh`.
- `openai-completions` has no fixed Prism chat wire shape for reasoning. If the generated OMP provider selects that API for a chat-wire model and thinking disappears, report a product regression rather than weakening the assertion. Messages-wire models rely on the per-model `anthropic-messages` override instead.
- Antigravity suppresses thought summaries on function-calling turns (upstream behavior); the assert script accepts their absence with a warning on tool turns only. For the matrix (no tools), thinking absence is not excused.
- Live mode needs a usable Prism config and credential store, and the matrix needs the user's existing Codex and Antigravity accounts. Missing credentials make the feature `VERIFIED_UNREACHABLE`, not passed; a per-pair upstream 401/403 is a pair FAIL, not unreachable.
- Never sign in, refresh credentials, edit accounts, or expose secrets during matrix runs. The sanitized daemon config in evidence strips `apiKeyRef`; credentials never enter evidence, and the sandbox copy is deleted by cleanup.
- Grok's `max_retries` default (100) makes client retries effectively unbounded: the outer timeout is the real session bound, which is why the matrix enforces a ceiling.
- The matrix refuses to start when a daemon already answers on its verify port (18787) and will not double-drive the user's shared instance. The default product daemon port is 10200.
