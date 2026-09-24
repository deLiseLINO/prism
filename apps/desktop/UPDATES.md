# Desktop updates

The private source repository is not the update server. Packaged applications check `https://releases.prism.sh/desktop`. That HTTPS path must expose the objects in the release bucket at `/desktop/` without authentication. Keep the bucket private if the HTTPS distribution provides public read access. Do not put storage or GitHub credentials in the application.

## Configure the release

1. Point `releases.prism.sh` at a public HTTPS distribution for the release bucket. Preserve object names and serve `latest*.yml` and `prerelease*.yml` without caching. The release job uploads installers before channel files.
2. Create an AWS role that GitHub Actions can assume using OIDC. Limit its trust policy to this repository and the release workflow. Grant `s3:PutObject` on the bucket's `desktop/*` prefix. Set repository variables `UPDATE_FEED_ROLE_ARN`, `UPDATE_FEED_REGION`, and `UPDATE_FEED_BUCKET`.
3. Supply a Developer ID Application certificate as repository secrets `MAC_CSC_LINK` and `MAC_CSC_KEY_PASSWORD`. For notarization, set `APPLE_ID`, `APPLE_APP_SPECIFIC_PASSWORD`, and `APPLE_TEAM_ID`. Supply a Windows code-signing certificate as `WIN_CSC_LINK` and `WIN_CSC_KEY_PASSWORD`. The release jobs reject missing credentials and verify the resulting installer signatures.
4. Run the release workflow with `dry_run: true` against a version tag. It builds the installers and validates their update metadata without publishing. After checking the artifacts, dispatch the workflow with `dry_run: false`. A tag push also publishes; do not push a release tag until the storage and signing credentials are ready.
5. Install one signed build, release a higher version, and check an update on macOS, Windows, and Linux AppImage. An existing build with updates disabled needs a manual reinstall first. Linux updates require launching the AppImage, not an extracted directory.

Stable builds publish both `latest*.yml` and `prerelease*.yml`, so prerelease users receive the final release. Prerelease builds update only `prerelease*.yml` and leave the stable channel untouched. The release job assembles the feed with `apps/desktop/scripts/assemble-update-feed.mjs` and verifies file sizes and SHA-512 checksums with `apps/desktop/scripts/verify-update-feed.mjs`. The public installer and update payloads can be downloaded without access to the private repository.

Linux AppImage updates use the feed's SHA-512 checksums but do not verify an independent publisher signature. Restrict writes to the bucket and its HTTPS distribution; anyone who can replace both a channel file and its payload can supply a matching checksum. Do not treat a passing checksum as proof of publisher identity.
