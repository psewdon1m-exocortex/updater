# updater

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

The updater has no service-specific `.env`. A root-owned registry maps a head
ID to that head's existing environment file:

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

`repositories.updater.url` is used only by the manual `updater update`
self-update command.

## Installation with a head

Kernel and Perimetr release bundles contain the updater binary, unit and
installer. After configuring the head `.env`, their `install.sh` installs both
the local updater and the head containers. The updater can also be installed
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
- the compose archive must match the SHA-256 stored in the selected manifest;
- the selected image is pulled by immutable digest;
- the operator download and server-side backup are created before mutation;
- existing persistent Docker volumes are preserved;
- local and optional public health checks gate success;
- a failed health check restores the previous image and imports the backup;
- request IDs are idempotent and job state survives updater restarts.
- daemon startup repairs a registered head container automatically when its
  updater socket directory still points at an obsolete bind-mount inode.
- Gryphon Linux updates resolve `repositories.gryphon.url`, verify the
  `exocortex.gryphon.release.v1` manifest and archive checksum, atomically swap
  `/usr/local/lib/gryphon/app`, and roll back when the client-socket health
  probe does not recover.
- Neptune remote updates use a separate root-owned bridge token and Unix-socket
  endpoint. They accept only a registered head ID plus a semantic Neptune
  version; repository resolution, manifest/checksum verification, atomic swap,
  health check, and rollback remain inside Updater.

The worker retains at most 20 finished jobs/backups and removes finished data
older than 30 days. The systemd journal is rate-limited to 200 messages per
30 seconds. These host-wide defaults are declared in `updater.service`, not in
a second service-specific `.env`.

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

`neptune install` bootstraps the host-wide daemon from the latest checksummed Linux release when it is absent and uses the same rollback-safe binary replacement for later upgrades. `neptune enroll` reads a 15-minute single-use Saturn code from standard input, creates isolated local tokens, registers the project, updates its existing `.env`, and recreates only that service container.

Installed service heads may invoke the equivalent enrollment through the authenticated local Unix socket. The API accepts only the registered head/project pair, a loopback export URL and a 32-character one-time Saturn code; it creates a durable background job and never stores the code. A missing Neptune installation is not bootstrapped from a service UI and still requires the host installer.

`updater update` is operator-triggered. It downloads the checksummed updater release,
atomically replaces the binary, restarts the systemd unit, verifies the Unix
socket health endpoint and restores the previous binary if verification fails.

The current six-service deployment, trust, recovery and acceptance contract is documented in [Deployment readiness](DEPLOYMENT_READINESS.md).
