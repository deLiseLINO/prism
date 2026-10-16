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

Anthropic Messages requests retain client `cache_control` markers and system block boundaries when routed to a Messages provider. No additional settings are required. Prism does not add cache markers to requests that omit them.

Chat providers may send `tool_calls: null` in stream chunks. Prism treats it as no tool-call update and preserves text and active tool calls. Non-null tool-call values must still be arrays.

Within streamed tool calls, null `id`, `name`, and `arguments` fields mean no update. Prism preserves the call identity and accumulated arguments. Non-null values in these fields must still be strings.
