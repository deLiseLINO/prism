# Grok and OMP client compatibility (live matrix)

Prism-generated configuration must work in the real client, not only parse as TOML or YAML. Grok must complete through the Responses endpoint. OMP must complete without an immediate protocol error and must expose model thinking when the user requests it. The live matrix extends this to the three application models a user selects inside the clients — Luna, Gemini 3.7 Flash, and GLM 5.3 — with one >=300-second session per supported client/model pair.

## Sub-features

- Grok loads the model aliases written by the Integrations screen.
- Grok completes one live Antigravity request without retrying.
- OMP loads the provider and models written by the Integrations screen.
- OMP completes one live Antigravity request with `--thinking high --print-thoughts`.
- OMP emits a final answer and a non-empty thinking block in structured output.
- OMP completes a real read-tool call and receives its result before the final answer.
- Client failures preserve stderr and the structured event stream.
- Live matrix: for each of `codex/gpt-5.6-luna`, `antigravity/gemini-3.7-flash`, `router/glm-5.3`, Grok and OMP each hold one session alive >=300 seconds with exit 0, a substantive final response, and zero transport errors.
- New-agent smoke: for each of Claude, Pi, opencode v1, opencode2, and Hermes, one headless live request through the configuration written by the Integrations screen must return a substantive answer with exit 0. The reference run answered `PONG` for all five clients through `codex/gpt-5.6-luna` (evidence `evidence/integrations-live/20260905-075636.5126/`): `claude -p ... --model claude-codex--gpt-5.6-luna`, `pi --provider prism --model codex/gpt-5.6-luna -p ...`, `opencode run --model prism/codex/gpt-5.6-luna ...`, `opencode2 run --standalone --model prism/codex/gpt-5.6-luna ...` (the background service does not start in an isolated home), `hermes chat -q ... --provider prism -m codex/gpt-5.6-luna --ignore-user-config`. Claude's request also proves the messages wire end to end: the CLI's background title generation uses `ANTHROPIC_MODEL`, so a broken default-model choice surfaces as a hang there, and its `[claude-code:unrecognized_model]` stderr line from title generation is noise, not a failure, as long as the answer and exit 0 are present.

## How to get to it (user POV)

Click Apply for Grok and OMP in Prism. Start each client normally, select its Prism model — for the three matrix models Grok uses `prism-codex-gpt-5-6-luna`, `prism-antigravity-gemini-3-7-flash`, `prism-router-glm-5-3`, and OMP uses `prism/codex/gpt-5.6-luna`, `prism/antigravity/gemini-3.7-flash`, `prism/router/glm-5.3` — and send a prompt that requests reasoning. The request must finish. OMP must render thinking before the final answer.

## Driving it with the real clients

Single-model probes:

```sh
PRISM_VERIFY_LIVE=1 \
  verify/scripts/integration-drive.sh
```

The helper uses only files created by the UI click. It runs Grok with the generated alias. It asks OMP to read a sandbox file with the real `read` tool, runs OMP in NDJSON mode with `--thinking high --print-thoughts`, then passes the event stream to `assert-omp-output.mjs`.

The full matrix:

```sh
verify/scripts/live-matrix.sh
```

It runs six pairs (2 clients x 3 models), each a sequence of long-generation prompts that must stay alive >=300 seconds total (floor `PRISM_MATRIX_MIN_SECONDS`, ceiling `PRISM_MATRIX_CEILING_SECONDS`), closed by one short marker request once the floor is reached. Router pairs run with reasoning effort off; antigravity pairs use shorter essays; failed attempts are retried up to three times and preserved under `pairs/<pair>-failed/`. Each pair records model ID, client, provider, leased account, start/end timestamps, elapsed, exit code, transport error count, marker count, final response length, stdout/NDJSON, stderr, and the daemon log slice. `assert-matrix-run.mjs` re-verifies the whole evidence run including the sha256 manifest.

A matrix pass requires, per pair:

- elapsed >= 300 seconds and exit 0;
- the unique marker exactly once in the final assistant text of the marker request, with >=1000 total assistant characters across the pair;
- zero transport errors (no `error`/`agent_error` events, no `upstream_transport` envelopes, no failed tool executions; corroborated by no account sliding to `cooling_down`/`soft_avoid` in the usage snapshots);
- daemon health 200 before and after.

## Gotchas

- `Working...` is progress UI, not proof of thinking.
- Reasoning token usage does not prove that OMP received a displayable thinking block.
- A final answer alone does not satisfy the single-model probe feature.
- `openai-completions` has no fixed Prism chat wire shape for reasoning. If the generated OMP provider selects that API and thinking disappears, report a product regression rather than weakening the assertion.
- Antigravity suppresses thought summaries on function-calling turns (upstream behavior); the assert script accepts their absence with a warning on tool turns only. For the matrix (no tools), thinking absence is not excused.
- Live mode needs a usable Prism config and credential store, and the matrix needs the user's existing Codex and Antigravity accounts. Missing credentials make the feature `VERIFIED_UNREACHABLE`, not passed; a per-pair upstream 401/403 is a pair FAIL, not unreachable.
- Never sign in, refresh credentials, edit accounts, or expose secrets during matrix runs. The sanitized daemon config in evidence strips `apiKeyRef`; credentials never enter evidence, and the sandbox copy is deleted by cleanup.
- Grok's `max_retries` default (100) makes client retries effectively unbounded: the outer timeout is the real session bound, which is why the matrix enforces a ceiling.
- The matrix refuses to start when a daemon already answers on 18787 — it will not double-drive the user's shared instance.
