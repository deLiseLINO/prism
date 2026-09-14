# Windows build (NSIS)

## What goes into the delivery

- `resources/icon.ico` — the Windows icon (16/32/48/64/128/256 inside one file).
- `resources/prismd/prismd-windows-amd64.exe` and `resources/prismd/prismd-windows-arm64.exe` — cross-builds of the daemon (`GOOS=windows`, `GOARCH=amd64`/`arm64`, `CGO_ENABLED=0`), produced by `build.mjs` together with the linux binaries.
- `electron-builder.yml`, section `win`: target `nsis`, unsigned (no certificate). Section `nsis`: `oneClick: false`, `perMachine: false`, `allowToChangeInstallationDirectory: true`, `createDesktopShortcut: true` (checked by default).

## Daemon binary placement

- Packaged app (Windows): the binary is looked up as `process.resourcesPath/prismd/prismd-windows-<arch>.exe`, where `<arch>` is the current process architecture (`process.arch`: `x64` or `arm64`) (`main/daemon/locate.ts`).
- Dev mode on Windows: `app.getAppPath()/resources/prismd/prismd.exe` (the host `go build` in `build.mjs` places exactly that one).
- `PRISMD_PATH` always overrides the auto-lookup.

## Local build (from the repo root)

```bash
npm install
npm run build --workspace @prism/desktop
```

Generates dist + all prismd binaries, including `prismd-windows-amd64.exe`.

## Building the NSIS installer

electron-builder builds the NSIS installer on macOS/Linux/Windows without wine: the NSIS target is fully cross-platform, the app contains no native win code (the daemon is cross-compiled by Go). Verified locally on macOS (arm64): the installer `release/Prism Setup 0.1.0.exe` builds successfully.

```bash
cd apps/desktop
npm run build                       # dist + resources/prismd/*
npx electron-builder --win --config electron-builder.yml
```

Result: `apps/desktop/release/Prism Setup <version>.exe` — one universal installer (x64 + arm64 inside, the right one is picked on the machine) + `latest.yml`/`blockmap` for auto-updates.

Notes:
- `electronVersion: 38.8.6` is pinned in electron-builder.yml, because eb does not resolve the `^38.0.0` range from package.json (keep them in sync when electron is updated).
- There is no signature (no certificate); electron-builder applies its zero signature to unsigned files — that is not a real signature, a certificate will need to be added later (`win.certificateFile` or env `CSC_LINK`).
- "signing with signtool.exe" in eb logs is a fake signature for unsigned builds; there is no real certificate.

## Regenerating icon.ico

```bash
cd apps/desktop/resources
magick icon.png -define icon:auto-resize=256,128,64,48,32,16 icon.ico
```

(any local ImageMagick; no runtime dependencies — the .ico is committed to the repository)

## CI (if locally it fails for some reason)

```yaml
- uses: actions/setup-node@v4
  with:
    node-version: 22
- run: npm install
- run: npm run build --workspace @prism/desktop
- run: npx electron-builder --win --config electron-builder.yml
  working-directory: apps/desktop
  env:
    GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

Works on both `windows-latest` and `ubuntu-latest` (NSIS cross-build); on macOS too (verified locally).
