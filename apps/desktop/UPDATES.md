# Desktop releases

Prism publishes installers through GitHub Releases in the public `deLiseLINO/prism` repository. The packaged updater reads release assets without a GitHub token. The release workflow does not use a separate update server or storage credentials.

## Publish a release

1. Make the repository public before distributing installers. GitHub Releases in a private repository are not an anonymous update feed.
2. Merge the release changes and set `apps/desktop/package.json` to the base version `X.Y.Z`, including for prereleases. Configure `MAC_CSC_LINK` and `MAC_CSC_KEY_PASSWORD` as GitHub Actions secrets for a single, persistent macOS code-signing certificate and private key. Use the same identity for every release. `MAC_CSC_LINK` is a base64-encoded PKCS#12 file; keep the unencoded private key out of the repository. The release job rejects missing signing credentials.
3. Create and push a new `vX.Y.Z`, `vX.Y.Z-beta.N`, or `vX.Y.Z-rc.N` tag on that commit. Pushing the tag does not publish: run the `Release` workflow manually and enter the full version.
4. Leave `dry_run: true` for the first run. Inspect the generated artifacts and checksums. Then run the workflow again with `dry_run: false` to publish the immutable GitHub Release. Do not reuse a tag whose release already exists.
5. After the workflow succeeds, inspect the GitHub Release assets and `SHA256SUMS`. Every release contains `latest.yml`, `latest-mac.yml`, `latest-linux.yml`, `latest-linux-arm64.yml` and their `beta` and `rc` counterparts. A `vX.Y.Z-beta.N` or `vX.Y.Z-rc.N` tag creates a prerelease. An RC build checks newer RC releases; a beta build checks beta and stable releases. The workflow updates the Homebrew cask for stable and RC releases, but not beta releases. An RC build must be reinstalled to switch to the stable channel.
6. Install an older published build, publish a higher version, and check downloading and applying the update on macOS, Windows, and Linux AppImage. A build made before updater support needs manual reinstall first. Linux self-update requires launching the AppImage from a writable directory. If that directory is not writable, updates are disabled; permission is checked again before the app quits to install.

The publish job verifies the sizes and SHA-512 values in the update manifests with `apps/desktop/scripts/verify-update-feed.mjs`. The update payloads and manifests are attached to the same immutable version release. Publishing credentials stay in GitHub Actions.

## macOS without a Developer ID

The release contains a signed app, unsigned DMGs, and signed app ZIPs for updates. Squirrel.Mac checks the new app against the installed app's designated code-signing requirement. The signing identity must stay the same across versions. An ad-hoc signature (`codesign -s -`) changes with the app's contents and cannot replace this identity. Do not re-sign a downloaded copy: that breaks the link to the next update.

A self-signed certificate is enough to establish update continuity, but Apple does not trust it as a Developer ID certificate. The app is not notarized. Download the correct DMG from the GitHub Release, compare its SHA-256 with `SHA256SUMS`, copy `Prism.app` into Applications, then try to open it. If macOS blocks it and you trust the source, follow [Apple's Open Anyway instructions](https://support.apple.com/en-us/102445) in **System Settings → Privacy & Security**. Do not disable Gatekeeper system-wide.

To make a new code-signing identity for CI, create one self-signed code-signing certificate with its private key and export them together to a password-protected PKCS#12 file. If exporting with OpenSSL 3, use `openssl pkcs12 -export -legacy`: macOS `security import` rejects its default PKCS#12 encryption. Store that file only in the `MAC_CSC_LINK` secret as base64, and store its export password in `MAC_CSC_KEY_PASSWORD`. Before releasing, confirm that both the old and new signed apps pass `codesign --verify --deep --strict` and that the new app satisfies the old app's designated requirement. Losing or rotating this private key breaks automatic updates from previously installed builds.

## Update trust

Windows and Linux installers are also unsigned. Their updater verifies SHA-512 from the GitHub Release metadata, but that does not prove publisher identity if someone can replace both the manifest and the installer. Protect repository write access and release publishing permissions. Windows may display an unknown-publisher warning. Do not present these builds as signed or notarized.
