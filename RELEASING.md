# updater releases

Updater releases use tags in the form `updater-vMAJOR.MINOR.PATCH`.

1. Run `go test ./...` in `updater/`.
2. Update the version notes in `CHANGELOG.md`.
3. Commit the release state and push `updater-vX.Y.Z`.
4. CI builds a static Linux amd64 binary, Debian package and a self-contained
   `updater-X.Y.Z-install.tar.gz` consumed by Kernel and Perimetr release jobs.
   It publishes SHA-256 files and build provenance.
5. Verify the GitHub release before using `updater update`.

Kernel Register must contain `repositories.updater.url`. The updater
self-update resolves only `updater-v*` tags and the
`updater-release.json` asset. It never follows a mutable branch.

Publish Updater before a Kernel or Perimetr release that pins it. Head-service
CI downloads the bundle from this repository and verifies its SHA-256 before
including it in a Compose bundle.
