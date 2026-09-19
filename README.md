# updater

> Documentation authority: the workspace-wide [Part 00](https://github.com/psewdon1m-exocortex/general/blob/main/PART_00_SYSTEM_UNIFICATION_SPECIFICATION.md)
> and its applicable Parts are normative. This repository documents
> Updater-specific details only; a conflict is corrected here and a material
> implementation difference follows the Part 00 divergence protocol.

## Required pre-push gate

After native checks and before every push, complete the checks required by
[Part 06 — Unified acceptance checklist](https://github.com/psewdon1m-exocortex/general/blob/main/PART_06_UNIFIED_ACCEPTANCE_CHECKLIST.md) and run the versioned policy in
`.github/pre-push-gate.json` through `scripts/pre-push-gate.py`. CI repeats the
gate on `main`. Security is always reviewed; backup/restore, updater, embedded
Documentation and affected technical docs are reviewed when relevant. Apply
SEO/GEO checks to intentionally public/indexable surfaces and concealment,
crawler and probe-resistance checks to private or authenticated surfaces.
Every area requires `PASS` evidence or a reasoned `N/A`.

## Required pre-release known-problem gate

Before a service-qualified release is finalized, evaluate every active ID in
[Part 12](https://github.com/psewdon1m-exocortex/general/blob/main/PART_12_KNOWN_DEPLOYMENT_AND_OPERATIONS_PROBLEMS.md) against the exact candidate. Retain
`known-problems-report.json` bound to the service revision, qualified tag,
immutable central-documentation revision and catalog digest. Missing, stale,
failed, unknown or unsupported `N/A` evidence blocks publication. This is a
normative release requirement; until the repository workflow generates and
enforces that report, the release pipeline remains an implementation gap.

`updater` is a local host tool for applying checksummed releases of Exocortex head
services. It is deliberately not a general central deployment service. The one
central-control bridge is deliberately narrow: the local Neptune daemon may ask
it to install one validated Neptune Linux version queued in Saturn Synchronization.

Every VPS that hosts Kernel, Perimetr, or another supported head has its own
updater process:

```text
operator browser -> head web UI -> local Unix socket -> updater -> local Docker
                                      |
                                      +-> Kernel Register (repository URL)
```

Kernel and Perimetr may be on different VPSs and behind different SNI names.
Their updaters never contact each other. Each updater can mutate only Compose
projects registered on its own host. If several heads share one VPS, one
updater process can serve all of them through separate registered head
profiles and separate control tokens.

Installing a second head on the same VPS does **not** start a second updater
daemon. The installer takes a host-wide lock, preserves a newer installed
binary, adds or updates the head profile in the shared registry, and restarts
the single `updater.service`. Both heads use the same Unix socket but have
different `UPDATER_HEAD_ID` and `UPDATER_CONTROL_TOKEN` values. Running two
independent updater daemons with the default paths is intentionally rejected
by the Unix-socket collision.

## Configuration

Updater has its own root-owned, mode-`0600`
`/etc/exocortex/updater/.env`. It contains only updater-daemon settings such as
its socket, state, retention and registry paths; it never absorbs another
service's secrets. A separate root-owned registry maps a head ID to that
head's existing, independently managed environment file:

```sh
sudo updater register-head kernel /opt/exocortex/kernel/.env
sudo updater register-head perimetr /opt/exocortex/perimetr/.env
```

The head environment supplies `KERNEL_URL`, `KERNEL_SERVICE_TOKEN`,
`UPDATER_CONTROL_TOKEN`, Compose paths, the image variable and health/restore
URLs. The repository URL is never duplicated there: Register stores its
`volt://` reference, and updater resolves that key through Kernel before use.
The verified cache contains references only, so a restarted updater requires
available Kernel and Volt to obtain actual values.

`UPDATER_IMAGE_VARIABLE` and `UPDATER_VERSION_VARIABLE` identify the head's
image and installed-version keys. Updater changes them in one atomic `.env`
rewrite and restores both values during rollback, so release discovery never
reports a successfully installed release as still pending.

`repositories.updater.url` is used by UI discovery, exact-version self-update
and the equivalent `updater update` command.

## Installation

Install one explicit immutable Updater release with Updater's own bootstrap
(replace `X.Y.Z`):

```sh
curl -fsSL https://github.com/psewdon1m-exocortex/updater/releases/download/updater-vX.Y.Z/bootstrap.sh | sudo sh
sudo chmod 600 /etc/exocortex/updater/.env
updater status
```

The Part 04 target is a prepare/edit/install boundary. Updater currently has no
operator-input field and its bootstrap installs the daemon immediately after
verification; this is a documented service-specific variation, not permission
for head-service bootstraps to start their applications. If an operator input
is added later, Updater must adopt the normal two-stage flow before release.

The protected release job keeps Updater's private signing key in GitHub
Secrets, derives its public counterpart and embeds only the public key in that
versioned bootstrap. On a clean host bootstrap creates
`/etc/exocortex/release-trust/updater.pem`, verifies
`updater-release.json` before trusting its artifact locations, and only then
accepts the signed installer's pinned Neptune and Gryphon public keys. The
installer writes all three keys under `/etc/exocortex/release-trust`, creates
Updater's own `.env`, and installs the daemon. Any existing mismatching key
fails closed. Installation requires no `scp`, manual release-key fingerprint
or separately downloaded public key.

## Installation with a head

Kernel and Perimetr release bundles may contain the updater binary, unit and
installer after their release CI has verified the pinned Updater release.
After configuring the head's own `.env`, their `install.sh` can install the
verified local updater and the head containers. It must preserve Updater's
independent trust and `.env` boundaries. The updater can also be installed
manually:

```sh
sudo ./updater/install.sh kernel /opt/exocortex/kernel/.env
```

If updater is absent, release discovery in the head UI still works, but the
Install button is disabled and the UI reports that the local updater is not
installed.

## Update guarantees

Head releases use module-scoped tags. Saturn is resolved only from
`saturn-vMAJOR.MINOR.PATCH`; a legacy repository-wide `vMAJOR.MINOR.PATCH` tag
is not an installable Saturn release. Saturn updates replace the `api`,
`worker` and loopback-only `web` services; Updater never starts or mutates the
server-managed Nginx.

- no arbitrary command, image or URL is accepted from a head;
- release metadata is accepted only from HTTPS GitHub repositories;
- Neptune and Gryphon trust is carried inside the installer whose manifest was
  verified by the public key embedded in Updater's exact-version bootstrap; a
  public key beside a helper artifact is never accepted as its trust source;
- the compose archive must match the SHA-256 stored in the selected manifest;
- the selected image is pulled by immutable digest;
- one signed-receipt ZIP is saved by the operator before mutation; no server archive is retained;
- existing persistent Docker volumes are preserved;
- local and optional public health checks gate success;
- a failed health check restores the previous image and imports the backup;
- request IDs are idempotent and job state survives updater restarts.
- daemon reconciliation repairs a running registered head automatically when
  its Updater, Neptune or Gryphon socket directory points at an obsolete
  bind-mount inode. The helper units also preserve their runtime directories
  across normal restarts, so an update does not ordinarily require container
  recreation.
- Gryphon Linux updates resolve `repositories.gryphon.url`, verify the
  `exocortex.gryphon.release.v1` manifest and archive checksum, atomically swap
  `/usr/local/lib/gryphon/app` together with the verified systemd unit, and
  roll back both when the client-socket health probe does not recover.
- Neptune remote updates use a separate root-owned bridge token and Unix-socket
  endpoint. They accept only a registered head ID plus a semantic Neptune
  version; repository resolution, manifest/checksum verification, atomic
  executable/unit replacement, health check, and rollback remain inside
  Updater.

The worker retains at most 20 finished job metadata records, bounded by 30 days.
ZIP bytes are cleared from RAM when an operation terminates. Later recovery
requires uploading the original operator-held ZIP. The systemd journal is rate-limited to 200 messages per
30 seconds. Unit-level safety defaults remain declared in `updater.service`;
`/etc/exocortex/updater/.env` is the only Updater environment file and head
settings remain in each head's own `.env`.

A single container replacement can cause a short connection interruption.
Running work in other services is not stopped. Processes that already resolved
their configuration may continue with in-memory values; fresh resolution waits
for Kernel and Volt.

## CLI

```text
updater serve
updater register-head <id> <env-file>
updater status
updater jobs
updater update [--head <id>]
updater neptune install --head <id>
updater neptune enroll --head <id> --project <id> --export-url <loopback-url>
updater neptune doctor
updater version
```

`neptune install` bootstraps the host-wide daemon from the newest Linux release
allowed by the already trusted, signed Neptune manifest when it is absent and
uses the same rollback-safe executable and systemd-unit replacement for later upgrades. It never
learns first-install trust from a key beside that release. `neptune enroll`
reads a 15-minute single-use Saturn code from standard input, creates isolated
local tokens, registers the project, updates its existing `.env`, and recreates
only that service container.

Installed service heads may invoke the equivalent enrollment through the authenticated local Unix socket. The API accepts only the registered head/project pair, a loopback export URL and a 32-character one-time Saturn code; it creates a durable background job and never stores the code. A missing Neptune installation is not bootstrapped from a service UI and still requires the host installer.

`updater update` is operator-triggered. It downloads the checksummed updater release,
atomically replaces the binary, restarts the systemd unit, verifies the Unix
socket health endpoint and restores the previous binary if verification fails.

The current six-service deployment, trust, recovery and acceptance contract is documented in [Deployment readiness](DEPLOYMENT_READINESS.md).

## Unified updates (protocol 2)

See [Update protocol, saved ZIP and first migration](docs/UPDATE-PROTOCOL.md).
The UI uses Updater **0.5.0**, an exact selected version, the standard ZIP saved
on the operator PC, and durable status/progress. Helper updates use the same
dialog without a backup. No update ZIP is retained on the application host.
