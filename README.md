# prism

Local proxy with a terminal UI, a CLI and a desktop app.

## Install

**Homebrew** (macOS, Linux)

```
brew install deLiseLINO/tap/prism
```

**Go** 1.27 or newer (macOS, Linux, Windows)

```
go install github.com/deLiseLINO/prism/cmd/prism@latest
```

**Archive.** Download `prism_<version>_<os>_<arch>` from the
[releases page](https://github.com/deLiseLINO/prism/releases), unpack it and put `prism` on your `PATH`.

**Desktop app.** Installers for macOS, Windows and Linux are on the releases page.
On macOS: `brew install --cask deLiseLINO/tap/prism-desktop`.

## Use

```
prism                 # start the daemon if needed and open the UI
prism status          # CLI commands, see `prism help`
prism service stop    # stop the background daemon
prism upgrade         # update prism (brew and go installs)
```

The daemon listens on `127.0.0.1:10200`. The UI, the CLI and the desktop app share it.

A `chat`, `responses` or `messages` provider can list more instances of the same service. Prism sends each request to the endpoint first and moves to the next address, in the given order, when the current one refuses the connection, does not connect within 10 seconds, or answers 429, 502, 503 or 504 before any output reaches the client. Other answers come back as they are, and nothing is retried once output has started. A failed address is tried last for the next 30 seconds; after a 429 it waits for the answer's `Retry-After` instead, up to 5 minutes. If every address fails, the client gets the last answer unchanged. All addresses use the provider's credential, and model listing uses them too. A provider with a single address behaves as before: its 429 reaches the client.

With `--spread` the provider uses all addresses at once instead of always starting at the endpoint. Each client session keeps to one address, chosen by hashing the session over the addresses, so the instance's account binding and prompt cache stay warm. The session is the client's `X-Session-Id` header (`session_id` on the responses route), or else the first user message. Requests without either rotate through the addresses. When the session's address fails or is cooling down, the session moves to the next address in its hash order and returns once the cooldown ends. The daemon logs each 429 cooldown and each session move.

```
prism providers add dia --wire chat --endpoint http://100.99.36.90:8085/v1 \
  --fallback-url http://100.99.36.91:8085/v1 --fallback-url http://10.0.0.5:8085/v1
prism providers edit dia --fallback-url http://10.0.0.5:8085/v1   # replace the list
prism providers edit dia --clear-fallback-urls                    # remove all fallback addresses
prism providers edit dia --spread                                 # pin sessions across all addresses
prism providers edit dia --no-spread                              # back to failover from the endpoint
```

Anthropic Messages requests retain client `cache_control` markers and system block boundaries when routed to a Messages provider. No additional settings are required. Prism does not add cache markers to requests that omit them.

Chat providers may send `tool_calls: null` in stream chunks. Prism treats it as no tool-call update and preserves text and active tool calls. Non-null tool-call values must still be arrays.

Within streamed tool calls, null `id`, `name`, and `arguments` fields mean no update. Prism preserves the call identity and accumulated arguments. Non-null values in these fields must still be strings.

When a Chat provider stops with `finish_reason: "length"`, Prism preserves the output-limit reason, text, reasoning, and token usage. It does not finish tool calls from that turn. Non-streaming responses omit those calls. Streaming clients may have received partial tool deltas and must not execute them after a length stop. Malformed tool arguments on normal completion still fail.

Provider HTTP failures retain their original status on non-streaming responses, their error details, and plain-text messages. An unavailable fallback does not replace an earlier provider failure. A rate or quota refusal takes precedence over a later authentication refusal before output begins.

After a provider rejects a credential, prism retains that refusal for later requests in the running daemon. Replacing the credential or resuming the account clears it. After a daemon restart, an account that still needs login receives a local login-required message instead of a quota error. Missing, paused, and unselected accounts return `account_unavailable`, not `quota_exhausted`.
