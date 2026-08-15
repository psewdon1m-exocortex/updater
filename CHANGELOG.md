# Changelog

## 0.2.1

- Restore keyless Sigstore bundles for release artifacts so Updater 0.1.x can
  cryptographically verify and install the current Updater before applying
  releases that use the simplified 0.2.x trust model.

## 0.2.0

- Simplify the early-stage release trust model to HTTPS, release identity,
  SHA-256 checksums and immutable OCI image digests.
- Remove the Cosign binary, Sigstore cache and detached-bundle requirement
  from installation, head updates and Updater self-update.
- Keep the 128 MiB head bundle limit and preserve the Unix-socket directory
  across service restarts.

## 0.1.2

- Accept head-service compose bundles up to 128 MiB. The former self-contained
  installer bundles were larger than the original 32 MiB limit.
- Preserve `/run/exocortex` across Updater service restarts so running head
  containers retain access to the recreated Unix socket.

## 0.1.1

- Give Cosign a writable operation-local home for its Sigstore trust cache
  while keeping `/root` inaccessible to the updater systemd sandbox.

## 0.1.0

- Local Unix-socket update API with registered head profiles.
- Signed release resolution through Kernel Register with last-known-good cache.
- Mandatory backups, immutable image digests, health checks and rollback.
- Manual, verified `updater update` self-update.
