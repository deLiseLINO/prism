# Client compatibility

A generated config must work in the real client. Parsing a file does not prove inference, a tool round trip, or visible thinking.

## Sub-features

- клиент grok loads aliases written by Integrations and completes a real request through the selected model.
- клиент omp loads generated provider entries and completes a real request. Its tool probe requires a read call and result before the final answer.
- A thinking assertion requires a displayed thinking block. Reasoning token usage alone is not that proof.
- The repeated-request matrix exercises selected client and model pairs over an aggregate duration. Each command invocation is fresh.
- Other supported clients need separate real inference runs. A structural config loader or two-client matrix does not cover them.

## How to get to it

Open Integrations and click Apply for the client. Enable Other agents if that row is hidden. Start the client, select a prism model written by Apply, and send a request.

## Driving it with the real clients

```sh
PRISM_VERIFY_LIVE=1 bash verify/scripts/integration-drive.sh
bash verify/scripts/live-matrix.sh
```

The integration driver sends a marker request with клиент grok. It asks клиент omp to read a sandbox file, then checks the final response and tool result. The single-model tool probe permits missing thinking and records that limitation.

The matrix takes requested selectors from `PRISM_MATRIX_GROK_MODELS` and `PRISM_MATRIX_OMP_MODELS`, or uses its default application-model pairs. It runs fresh commands until the aggregate duration reaches `PRISM_MATRIX_MIN_SECONDS`, default 300, and finishes with a marker request. Its ceiling bounds each client invocation, including retries. The request-count limit bounds the number of invocations; there is no total pair deadline.

Retain the command, structured output, stderr, attempt results, and health evidence for every pair. A passing marker after failed attempts does not erase those failures. The matrix cannot prove a single uninterrupted session, because it launches a new process for each request.

An uninterrupted-session proof needs one client process or an explicitly resumed session with a recorded session identity. Hold that session for the required duration, verify it continues after interruptions, and preserve its final output. This remains separate coverage.

## Gotchas

- A progress message such as `Working...` is not thinking evidence.
- Client retries can hide an upstream failure. Preserve failed attempts and distinguish a final successful request from a failure-free run.
- Requests using tools can differ from text-only requests in how thinking is returned. Do not generalize one result to the other.
- Missing credentials or a client binary make the relevant run unavailable. A started request that returns an upstream error is a failure.
- The daemon may refresh only the staged credential copy. Never complete a login or mutate the source accounts during verification.
- Raw copied state and unrestricted client logs can contain private data. Keep them private and publish only reviewed, nonsecret evidence.
- Refuse pre-existing daemon or CDP listeners. Do not drive a user's shared app to fill missing coverage.
