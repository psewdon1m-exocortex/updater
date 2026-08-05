# Changelog

## 0.1.1

- Give Cosign a writable operation-local home for its Sigstore trust cache
  while keeping `/root` inaccessible to the updater systemd sandbox.

## 0.1.0

- Local Unix-socket update API with registered head profiles.
- Signed release resolution through Kernel Register with last-known-good cache.
- Mandatory backups, immutable image digests, health checks and rollback.
- Manual, verified `updater update` self-update.
