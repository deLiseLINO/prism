# Golden records

`responses-core.protocol.request-shape.json` pins the exact wire request the
openai-chat codec-under-test must emit for the
`responses-core.protocol.request-shape` case of the vendored CL-00 authority
(`cmd/prism-wirecheck/fixtures/protocol-v1-cases.json`, `sourceCommit`
`3ad5bb6bd3f76f6879d84b78ea39edd3e01ec296`).

Derivation: the reference implementation at
`src/adapters/openai-chat.ts` (`buildRequest`) and `src/lab/conformance/`
(`fixture-provider.ts`, `fixture-providerConfig("openai-chat")`) at that
commit renders the adapter vector

```
{"modelId":"fixture-model","context":{"messages":[{"role":"user","content":"PING","timestamp":0}]},"stream":false,"options":{"temperature":0}}
```

as the body `{"model":"fixture-model","messages":[{"role":"user","content":"PING"}],"stream":false,"temperature":0}`
(102 bytes, no trailing newline, ES6 `JSON.stringify` byte order) with headers
`Content-Type: application/json` then `Authorization: Bearer fixture-key`, sent
to `https://api.openai.com/v1/chat/completions`. The byte parity contract is
design `design/prism-wirecheck-conformance-canon-first.md` §3.4.

Regenerate when the pinned reference moves: re-vendor the fixture, re-derive
from the new `sourceCommit`, and record the new commit here.

The five `codex-core.protocol.*.json` records pin the codec requests for the
codex-core suite of the same authority commit. Derivation follows the
reference `src/lab/conformance/executor.ts` case handlers and the
`fixture-provider.ts` base URLs (chat `https://api.openai.com/v1`, responses
`http://127.0.0.1:1/v1`):

- `apply-patch-turn`: second-turn chat request for the custom apply_patch
  round trip (assistant tool_calls lowered to a function call with the
  `{"input": ...}` envelope, then the tool result); the first upstream record
  is the literal PING request from the reference handler.
- `tool-continuation`: responses passthrough body with the prepended
  `function_call` and the paired `function_call_output`.
- `previous-response-replay`: expanded responses body (stored input + output,
  then the new input) with `previous_response_id` dropped and `store` kept.
- `structured-output`: chat body with `response_format` derived from
  `text.format` json_schema.
- `compaction-and-special-items`: chat body after exotic items
  (`context_compaction`, `local_shell_call`, `tool_search_output`) are skipped
  with typed warnings per the unit-2 design; the paired `function_call_output`
  emits the tool message.
