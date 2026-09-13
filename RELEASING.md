# updater releases

This document specializes [Part 05 — CI/CD and release
security](https://github.com/psewdon1m-exocortex/general/blob/main/PART_05_CI_RELEASES_AND_LOCAL_UPDATES.md) for this service. If
the two documents differ, Part 05 is authoritative.

Updater releases use tags in the form `updater-vMAJOR.MINOR.PATCH`. The
version sequence starts at `0.0.1`. A plain tag such as `v0.0.1` invokes
verification-only CI and must not publish or mutate a release; only the
service-qualified `updater-v...` namespace may invoke the release workflow.

> Current implementation gap (2026-09-13): `ci.yml` does not yet listen to
> plain `v*` tags. A separate CI change is required before a plain tag can be
> used as verification evidence; qualified Updater releases remain gated by the
> protected release workflow.

1. Run `go test ./...` in `updater/`.
2. Update the version notes in `CHANGELOG.md`.
3. Commit the release state and push `updater-vX.Y.Z`.
4. CI builds a static Linux amd64 binary, Debian package and a self-contained
   `updater-X.Y.Z-install.tar.gz` consumed by Kernel and Perimetr release jobs.
   The protected signing job reads Updater's private release key only from
   GitHub Secrets, signs `updater-release.json`, derives the public counterpart
   and embeds only that public key in the versioned standalone `bootstrap.sh`.
   CI verifies that bootstrap can create
   `/etc/exocortex/release-trust/updater.pem` and reject a bad manifest before
   it publishes the installer. That signed installer also carries the pinned
   public Neptune and Gryphon keys used by typed helper installs. CI publishes
   SHA-256 files, keyless Sigstore bundles and build provenance. No private key
   or trust-on-first-use key downloaded beside a manifest is accepted.
5. Verify the GitHub release before using `updater update`.

Kernel Register must contain `repositories.updater.url`. The updater
self-update resolves only `updater-v*` tags and the
`updater-release.json` asset. It never follows a mutable branch.

Do not remove `updater-release.json.sigstore.json`: Updater 0.1.x requires that
bundle to verify the release before it can self-update to 0.2.x. Current
Updater versions use HTTPS, checksums and immutable digests after that bridge.

Publish Updater before a Kernel or Perimetr release that pins it. Head-service
CI downloads the bundle from this repository and verifies its SHA-256 before
including it in a Compose bundle.

The release bootstrap also creates Updater's separate mode-`0600`
`/etc/exocortex/updater/.env`. Do not distribute release trust with `scp`, a
manual fingerprint ceremony or a public key downloaded beside the manifest.
