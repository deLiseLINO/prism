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

## Editing this README

Do not add to this README without asking the maintainer first.
