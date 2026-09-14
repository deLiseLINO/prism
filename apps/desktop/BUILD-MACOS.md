# Prism macOS distribution build

Documents the local unsigned dmg build, Gatekeeper behavior, and the
plan for when an Apple Developer account exists.

## How to build

Requirements: macOS, Node.js, Go, Xcode Command Line Tools
(`xcode-select --install`).

From the monorepo root:

```sh
npm install                                   # install dependencies (once)
npm run build --workspace @prism/desktop      # esbuild main/preload/renderer + go build prismd
cd apps/desktop
npx electron-builder --mac dmg                # builds a dmg for the current architecture
# or both architectures at once:
npx electron-builder --mac dmg --arm64 --x64
```

What happens:

1. `build.mjs` compiles the Go daemon `prismd` for every supported
   platform (`resources/prismd/prismd-{darwin,linux}-{arm64,amd64}`) —
   the main process picks the right binary via `process.platform`/`process.arch`
   (`main/daemon/locate.ts`), so an arm64 Mac can build a working
   x64 disk and vice versa. It also builds the JS bundles (`dist/`) and webui.
2. `electron-builder` packages the Electron app and produces the dmg.
   `extraResources` places `prismd/` and `webui/` into `Prism.app/Contents/Resources/`.

Result: `apps/desktop/release/Prism-<version>-{arm64,x64}.dmg`.

Architectures are two separate dmgs, not universal: each file is half
the size (Electron and the Go daemon are not duplicated per arch), and
GitHub Releases and the brew cask work naturally with arch-specific
artifacts.

Verifying the built dmg:

```sh
hdiutil attach -nobrowse -readonly apps/desktop/release/Prism-0.1.0-arm64.dmg
ls "/Volumes/Prism 0.1.0-arm64"                         # Prism.app + Applications
file "/Volumes/Prism 0.1.0-arm64/Prism.app/Contents/Resources/prismd/prismd-darwin-arm64"
# → Mach-O 64-bit executable arm64
hdiutil detach "/Volumes/Prism 0.1.0-arm64"
```

## No signature — what it means for users

The app is **unsigned and not notarized** (no Apple Developer account,
99$/year). This is a deliberate choice: `mac.identity: null` in
`electron-builder.yml` explicitly disables signing.

### Gatekeeper when installing from the dmg

On first launch of `Prism.app` copied from the dmg, macOS shows the
warning "Prism" cannot be opened because the developer cannot be
verified. The user workaround:

- **Right-click Prism.app → Open → Open** in the dialog.
  The confirmation is asked once; afterwards the app launches as
  usual. (On macOS Sequoia+ the same is available via
  System Settings → Privacy & Security → Open Anyway,
  if the dialog was already shown.)
- Technically: the right-click stores an approval in Launch Services,
  so Gatekeeper no longer blocks this exact app.

A terminal alternative (not recommended to advertise to ordinary
users): `xattr -dr com.apple.quarantine /Applications/Prism.app`.

### How the brew install differs

The Homebrew cask (`packaging/homebrew/`) downloads the same unsigned
dmg, but on install it **strips the quarantine flag**
(`com.apple.quarantine`) from the app it copies. So launching the
`brew install --cask …/prism` version never shows the Gatekeeper
warning at all. Functionally it is the same app with the same
permissions; the only difference is the missing quarantine attribute.
This is NOT the same as a signature: the user trusts the tap, not a
cryptographic developer signature.

### How this differs from a signed app

A signature (Developer ID Application) plus notarization (Apple scans
the binary for malware) give: a first launch without any warning,
normal upgrades over old versions (Team ID match),
auto-update trust (`electron-updater`), and no blocks under deeper
provider checks (for example, the macOS Application Firewall
and MDM policies).

## When an Apple Developer account exists

1. In `electron-builder.yml`, section `mac`:
   - drop `identity: null`;
   - set the identity, for example:
     ```yaml
     mac:
       identity: "Developer ID Application: <Name> (<TEAMID>)"   # or env CSC_NAME
       notarize: true                                            # electron-builder 24+ notarizes itself
       hardenedRuntime: true
       entitlements: build/entitlements.mac.plist
       entitlementsInherit: build/entitlements.mac.plist
     ```
     (for a basic Electron app hardenedRuntime: true is enough;
     entitlements are needed once JSS/OpenSSL or similar requirements appear.)
2. CI secrets: `APPLE_ID` / `APPLE_APP_SPECIFIC_PASSWORD` /
   `APPLE_TEAM_ID` (or `APPLE_API_KEY` + `APPLE_API_ISSUER` for the
   App Store Connect API — preferred).
3. Notarization requires the staple step to pass:
   electron-builder runs `stapler staple` automatically with
   `notarize: true`.
4. After that, Gatekeeper treats the brew install and the manual dmg
   install equally calmly — no more warnings.

## Known limitations of the current build

- `publish` in `electron-builder.yml` points at the nonexistent
  `https://releases.prism.sh/desktop` (someone else's zone, left alone).
  Auto-updates through it do not work until the move to GitHub Releases.
- The first launch after installing from the dmg requires the
  Gatekeeper workaround (see above) until a signature exists.
