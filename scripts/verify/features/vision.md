# Vision: image input through the sidecar

Text-only models receive image descriptions from a vision-capable sidecar model; image-capable models receive images directly.

## Wiring

- Main traffic rides the provider's configured wire (router: chat).
- Images only work on the upstream responses endpoint, so the sidecar target lives on its own provider with `wire: responses` (chat-resp), never by flipping the main provider's wire.
- The sidecar description turn streams; the upstream rejects non-streaming image requests.

## Verify

1. `curl http://127.0.0.1:10200/api/v1/providers` — router wire stays `chat`, chat-resp wire `responses`.
2. POST an image request (responses shape, `input_image`) to a text-only model: answer must describe the image, HTTP 200.
3. POST the same to the sidecar target's provider directly (chat-resp/gpt-5.6-luna): HTTP 200.

## Never

- Never edit `~/.prism/prism.json` by hand (jq/ruby/sed) — raw edits bypass the generation counter and clobber concurrent manager writes.
- Never flip the router provider wire to make vision work — it breaks codex chat traffic.
- All changes go through the management API: `PUT /api/v1/providers/{id}`, `PUT /api/v1/vision-sidecar`, with `expectedGeneration`.
