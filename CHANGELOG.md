# Changelog

## 0.5.0 — unreleased

- Standardize update discovery and durable jobs for heads and shared helpers.
- Require one saved standard ZIP and a signed receipt before a head update;
  keep rollback bytes in RAM/tmpfs and persist only recovery metadata.
- Verify the exact running version after activation. Read released Gryphon's
  in-process version from its admin status socket for backward compatibility.
- Migrate retained legacy backups and recover interrupted jobs without copying
  application secrets into deployment metadata.

## 0.4.9

- Permit atomic replacement of Saturn's `/etc/vault/.env.production` inside
  the otherwise strict Updater systemd filesystem sandbox.

## 0.4.8

- Stop requiring or applying Saturn's retired embedded Caddy configuration;
  Saturn releases now update Compose only while server Nginx remains
  operator-owned.
- Preserve rollback compatibility with legacy Saturn deployment snapshots
  without recreating the retired Caddyfile.

## 0.4.7

- Preserve Neptune and Gryphon runtime directories across service restarts so
  running containers retain their Unix-socket bind mounts.
- Install verified helper systemd units together with helper upgrades and roll
  back both the unit and executable/application if activation fails.
- Detect and repair stale Updater, Neptune and Gryphon socket-directory mounts
  for every running registered head.

## 0.4.6

- Limit Kernel resolution to supported release repository URLs and Saturn enrollment coordinates; never retrieve unrelated application credentials. Reject Kernel HTTP redirects before forwarding credentials.

- Authorize signed Chronos/Laboratory release scopes and reuse shared helpers with explicit consumer capabilities.
- Support Chronos Neptune/Gryphon and Laboratory Neptune lifecycle through head interfaces.
- Apply signed head Compose/env defaults while retaining operator values and full rollback state.
- Add head update/failure recovery and capability regressions; preserve migrations from 0.4.3 and 0.4.4.

Normative behavior is governed by [Part 00 — system unification
specification](https://github.com/psewdon1m-exocortex/general/blob/main/PART_00_SYSTEM_UNIFICATION_SPECIFICATION.md); changelog
entries are historical evidence and do not override it.

## 0.4.5

- Discover and fetch Gryphon releases only from the canonical
  `gryphon-vMAJOR.MINOR.PATCH` namespace.
- Discover and fetch the Linux artifact from Neptune's unified
  `neptune-vMAJOR.MINOR.PATCH` release.
- Reject the superseded platform-qualified helper tag namespaces.

## 0.4.4

- Restore group-readable Neptune project and credential files even when a head
  installer invokes enrollment under a restrictive umask.
- Force-recreate only the enrolled head service so atomically replaced Neptune
  credentials are mounted immediately without restarting unrelated services.

## 0.4.3

- Publish a standalone exact-version bootstrap with embedded RSA release trust.
- Require service trust to be pinned before release verification; a key beside
  a manifest is never accepted as a first-install trust anchor.
- Preserve the Updater, Neptune and Gryphon public trust files through head
  installs, Debian installs and Updater self-update, and own a separate
  root-only Updater environment.

## 0.4.2

- Bootstrap and pin missing public release keys from the same HTTPS GitHub
  release before verified first installation of Neptune or Gryphon.
- Carry the Updater public key inside the signed installation bundle so a new
  host needs no manual release-trust provisioning.

## 0.4.1

- Restart Saturn's loopback-only `web` service during update and rollback;
  public TLS and ingress remain owned by the server-managed Nginx.

## 0.4.0

- Resolve Saturn releases exclusively from module-scoped
  `saturn-vMAJOR.MINOR.PATCH` tags.
- Preserve the existing signed manifest, immutable image, backup and rollback
  verification boundaries while rejecting the legacy unscoped `v` tag.

## 0.3.0

- Bootstrap the host-wide Neptune Linux daemon from its checksummed release stream.
- Redeem short-lived Saturn enrollment codes and register isolated project credentials with one command.
- Add `updater neptune doctor` and permit rollback-safe Neptune binary replacement from the hardened service.
- Add a token-isolated local bridge used by Neptune to apply an update queued in Saturn Synchronization; the endpoint can replace only the Neptune Linux component.

## 0.2.2

- Detect head containers that retained an obsolete updater socket directory
  bind mount and force-recreate only those services when the daemon starts.

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
