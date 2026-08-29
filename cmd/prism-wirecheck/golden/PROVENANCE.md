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
