# Changelog

## 0.1.2

- Accept signed head-service compose bundles up to 128 MiB. Kernel and Perimetr
  bundles include the verified Updater/Cosign installer and are larger than the
  former 32 MiB limit.
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
