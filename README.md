# updater

`updater` is a local host tool for applying checksummed releases of Exocortex head
services. It is deliberately not a central deployment service.

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
URLs. The repository URL is never duplicated there: updater reads
`repositories.<service>.url` from Kernel Register and uses its verified
last-known-good Register cache if Kernel is temporarily unavailable.

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

- no arbitrary command, image or URL is accepted from a head;
- release metadata is accepted only from HTTPS GitHub repositories;
- the compose archive must match the SHA-256 stored in the selected manifest;
- the selected image is pulled by immutable digest;
- the operator download and server-side backup are created before mutation;
- existing persistent Docker volumes are preserved;
- local and optional public health checks gate success;
- a failed health check restores the previous image and imports the backup;
- request IDs are idempotent and job state survives updater restarts.

The worker retains at most 20 finished jobs/backups and removes finished data
older than 30 days. The systemd journal is rate-limited to 200 messages per
30 seconds. These host-wide defaults are declared in `updater.service`, not in
a second service-specific `.env`.

A single container replacement can cause a short connection interruption.
Running work in other services is not stopped; services that depend on Kernel
continue using their last-known-good Register revision.

## CLI

```text
updater serve
updater register-head <id> <env-file>
updater status
updater jobs
updater update [--head <id>]
updater version
```

`updater update` is operator-triggered. It downloads the checksummed updater release,
atomically replaces the binary, restarts the systemd unit, verifies the Unix
socket health endpoint and restores the previous binary if verification fails.
