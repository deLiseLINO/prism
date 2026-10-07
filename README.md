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

## Request translation

Chat, Responses, and Messages ingress preserve JSON numeric spellings in tool schemas and replayed arguments. Resend full Responses history instead of using `previous_response_id`.

Custom tools use a required string `input` wrapper on Chat routes and in buffered Messages replies. Responses routes preserve custom result arrays, including empty arrays and supported images. Vision descriptions replace images without changing string-valued custom results. Unsupported attachments and tool-result parts are rejected rather than dropped.

Tool subsets, explicit reasoning disablement, parallel-call controls, verbosity, and sampling are forwarded when the target format represents them. Messages routes reject unsupported verbosity and sampling with extended thinking. Forced tool choice omits incompatible thinking. Cline routes use Chat format.

Configured upstream URLs may name an API root or an endpoint. Versioned roots such as `/v1`, `/v4`, and `/v1beta` are retained; unversioned roots receive `/v1`.

## Stream waiting

Each provider accepts an optional `wait` object in its configuration and management API writes:

```json
{
  "wait": {
    "firstProgressMs": 900000,
    "idleMs": 300000
  }
}
```

`firstProgressMs` limits waiting for the first useful output, including connection and response headers.
`idleMs` limits silence after output begins. It does not run before the first useful output.
Text, reasoning, tool input, and output items count as progress. Heartbeats and response-start notices do not.
An output-item start, including a reasoning item with no text yet, starts the idle limit.
Time spent sending an event to a slow client does not consume the upstream waiting limit.

Each omitted field defaults to 300000 milliseconds. An explicit zero disables that limit.
If the first limit is disabled, the idle limit starts only after useful output arrives.
If both limits are disabled, waiting is unbounded unless the provider has a separate total request deadline.
Negative values and values that overflow a duration are rejected.

A management write without `wait` preserves existing settings. Supplying `wait` replaces the object.
Sending `"wait": {}` restores both defaults. The UI does not expose these settings yet.

These limits apply to streamed upstream requests. Chat and Responses requests for a single JSON reply do not use them.
Codex, Antigravity, and Messages upstreams always use streams, including for a buffered client reply.
The separate Antigravity per-attempt deadline is unchanged.
A waiting limit ends the turn as `upstream_stall` in the response, request history, and usage.
It does not replay the request on another provider. A client disconnect remains `client_closed`.

## Token usage

Upstream token counts must be exact nonnegative integers within `int64`.
Integral decimal and exponential JSON numbers are accepted without float conversion.
Invalid counts, overflowing sums, conflicting totals, and invalid cache or reasoning details fail the response.
An invalid update retains the last valid usage snapshot.

Chat and Responses cache and reasoning counts are included in input and output respectively.
Messages input includes uncached input plus cache reads and writes. Its cache fields and input deltas may be null.
Antigravity reasoning counts are not limited by output tokens.
Usage storage keeps cache writes for requests and attempts and migrates existing SQLite databases on open.

