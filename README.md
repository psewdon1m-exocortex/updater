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

`updater` is a local host tool for applying verified releases of Exocortex head
services and shared host agents. One daemon serves every registered head on the
host. Root operators install, check and update Updater, Neptune, Gryphon and
Wyvern through `sudo updater tui`. A narrow Neptune remote bridge still handles
commands accepted before the service-facing update controls were removed; new
release operations originate in the host TUI.

Every VPS that hosts Kernel, Perimetr, or another supported head has its own
updater process:

```text
operator browser -> head web UI -> local Unix socket -> updater -> local Docker
                                      |
                                      +-> host Kernel machine connection (host release URLs)
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
URLs. Application release discovery follows that service's scoped Register
configuration. For Updater and the three shared host agents, release source
belongs to the host: `/etc/exocortex/updater-host.json` holds Updater's own
Kernel URL, host ID, protected machine-token file path, and per-component TUI
fallback URLs. A live authenticated Kernel Register result takes priority.
Only a transport outage or unconfigured host connection permits the fallback;
invalid reachable data and verification failures stop the operation.

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
accepts the signed installer's pinned Neptune, Gryphon and Wyvern public keys.
The installer writes all four keys under `/etc/exocortex/release-trust`, creates
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
- Neptune checks and new updates are initiated in `sudo updater tui` on the
  host. The root-owned remote bridge remains for commands accepted before the
  service update controls were removed; service tokens cannot start new
  Neptune release checks or updates. Repository resolution, manifest/checksum
  verification, atomic executable/unit replacement, health check and rollback
  remain inside Updater.

The worker retains at most 20 finished job metadata records, bounded by 30 days.
Retention is enforced on daemon startup and every minute, including jobs
created from the host TUI; unfinished jobs and recovery material needed by an
active operation are not pruned.
ZIP bytes are cleared from RAM when an operation terminates. Later recovery
requires uploading the original operator-held ZIP. The systemd journal is rate-limited to 200 messages per
30 seconds. Unit-level safety defaults remain declared in `updater.service`;
`/etc/exocortex/updater/.env` is the only Updater environment file and head
settings remain in each head's own `.env`.

A single-container update can briefly return HTTP 502 from the public reverse
proxy while the target API is being replaced. This does not determine the
result of the durable Updater job: reconnect, read its terminal state and
verify the running version before starting another update.
Running work in other services is not stopped. Processes that already resolved
their configuration may continue with in-memory values; fresh resolution waits
for Kernel and Volt.

## CLI

The built-in terminal console manages this host's Updater, Neptune, Gryphon and Wyvern:

```sh
sudo updater tui
updater tui --demo
updater tui --no-color --demo
```

Each Updater, Neptune, Gryphon and Wyvern screen owns its independent host
recovery pipeline. Create only the identities for services installed on that
host, then enter that service's one-time setup code in its **Configure … recovery storage** action. Saturn's HTTPS origin comes from Updater's own Kernel Register. A saved origin supports outages; an explicit advanced origin override supports recovery. Updater exchanges each code
independently and keeps the resulting producer token in a separate root-owned
file. **Create … recovery archive** quiesces only the selected service, creates
one authenticated encrypted `.exorecovery` file and uploads it through Saturn
Gateway:

```text
backups/updater/<server-id>/<year>/<month>/<day>/...
backups/neptune/<server-id>/<year>/<month>/<day>/...
backups/gryphon/<server-id>/<year>/<month>/<day>/...
backups/wyvern/<server-id>/<year>/<month>/<day>/...
```

When configured, `updater`, `neptune`, `gryphon` and `wyvern` use distinct
folders directly under `backups`; a missing service needs no empty identity or
folder. The stable server ID partitions hosts inside each matching folder.

New archives use an independent generated recovery key for each service, stored under `<StateDir>/recovery-keys/<service>.json` with mode 0600. Use **Export recovery key to private file** once and keep that file outside the host; producer-token rotation does not change the key. The operator API never returns the key. To restore, download one archive through the owner file manager, install the matching trusted binary/unit, and choose **Restore … recovery archive** with an exported key file, or leave the key path empty to use this host’s key. **Restore legacy archive with passphrase** reads existing password-encrypted scoped archives. The archive header is cryptographically bound to its
service; restoring one service transactionally replaces only that service's
roots and cannot erase neighboring helper state. The former Saturn browser
form and service-facing Updater host-recovery route are intentionally absent.
Legacy combined v1 archives remain accepted by the root CLI for migration;
new exports always use the selected service's scoped format.

Use arrows, Enter and Esc; Tab moves between form fields. The demo uses
synthetic data and needs neither root nor installed services. In normal mode,
the console connects to the separate root-only operator socket. Install the
matching Updater binary and systemd unit before using it. Accepted jobs continue
after the SSH session closes; reopen operation history to observe their result.
Each component has its own editable fallback repository. The Updater section
also configures its machine connection to Kernel using a private token-file
reference. Checks and host installations work with zero registered heads.
Neptune schedules are edited in each owning service's Settings; Saturn retains
the authoritative policy revision and fleet observation. Wyvern has shared runtime diagnostics,
Kernel/Adapter management, client grants and signed lifecycle operations.
Its external configuration remains authoritative in Kernel/Volt; updating the
runtime never rolls those services back.
See [Terminal console](docs/TUI.md) for actions, trust boundaries, terminal
compatibility and verification. An ordinary SSH PTY is required; Termius itself
does not need an Exocortex plugin.

```text
updater serve
updater register-head <id> <env-file>
updater status
updater jobs
updater update [--head <id>]
updater host configure-kernel --url <https-origin> --token-file <protected-file> [host-id]
updater neptune install [--head <id>]
updater neptune install --bundle <signed-helper-directory>
updater gryphon install [--head <id>]
updater gryphon install --bundle <signed-helper-directory>
updater gryphon link --head <id>
updater neptune enroll --head <id> --project <id> --export-url <loopback-url>
updater neptune doctor
updater version
```

`gryphon link --head` provisions one registered head's scoped Gryphon client;
the optional `--head` on the CLI `gryphon install` also provisions that client
after host installation. Adapter registration and the owner's one-time
`/link CODE` pairing occur only in the root TUI. Host TUI install/check/update
operations need no head selection.

`neptune install` bootstraps the host-wide daemon from the newest Linux release
allowed by the already trusted, signed Neptune manifest when it is absent and
uses the same rollback-safe executable and systemd-unit replacement for later upgrades. It never
learns first-install trust from a key beside that release. `neptune enroll`
reads a 15-minute single-use Saturn code from standard input, creates isolated
local tokens, registers the project, updates its existing `.env`, and recreates
only that service container.

Installed service heads may invoke the equivalent enrollment through the authenticated local Unix socket. The API accepts only the registered head/project pair, a loopback export URL and a 32-character one-time Saturn code; it creates a durable background job and never stores the code. On a new deployment the consuming installer ensures Neptune. If the agent is absent on an older or damaged host, repair or root TUI installation restores it before enrollment.

`updater update` is operator-triggered. It downloads the checksummed updater release,
atomically replaces the binary, restarts the systemd unit, verifies the Unix
socket health endpoint and restores the previous binary if verification fails.

The current six-service deployment, trust, recovery and acceptance contract is documented in [Deployment readiness](DEPLOYMENT_READINESS.md).

## Unified updates (protocol 2)

See [Update protocol, saved ZIP and first migration](docs/UPDATE-PROTOCOL.md).
The UI uses Updater **0.6.14**, an exact selected version, the standard ZIP saved
on the operator PC, and durable status/progress for application releases.
Updater, Neptune, Gryphon and Wyvern release operations use the root TUI.
No update ZIP is retained on the application host.

Updater 0.6.6 reports the specific host release-check failure in the root TUI,
retries brief GitHub release API interruptions, and applies job-history
retention to host TUI operations as well as application updates.

Updater 0.6.8 first publishes Window support as a shared, independently signed host diagnostic
agent. The root TUI manages its release source, installation, pairing, timed
read grant, emergency revocation and observed operator shell. Window has no
application consumer dependencies in this release.
Updater 0.6.9 also supports Window's exact-version first install on a host
whose reachable Kernel Register has no Window repository key. Rerun the signed
Window bootstrap after updating Updater; ordinary TUI release checks still
require that Register key or an unavailable Kernel connection with a configured
fallback. See [deployment readiness](DEPLOYMENT_READINESS.md).
The 0.6.8 signed install archive keeps the 0.6.6 member set so an existing
Updater can self-update; its installer embeds the pinned Window public key.
The release workflow runs CI and the pinned Part 12 gate before exposing its
signing key, compares signed code with the checked candidate, verifies signed
assets, and checks anonymous downloads before attaching the final report.

Updater 0.6.5 prepares and permits Wyvern's dedicated journald policy directory
inside the daemon sandbox. A failed Wyvern activation can then restore the
previous managed runtime without an interactive repair command.

Updater 0.6.4 waits for Saturn's Compose readiness window after replacement and
rollback. If readiness still fails, the durable job records the HTTP status and
safe database, storage and worker check codes when the endpoint supplies them.

Updater 0.6.0 checks deployment and environment directories for sandbox write
access before recording a host mutation. If a rollback fails, the job retains
both the original update error and the rollback error. A signed same-version
self-update and the installer also repair a stale `updater.service` unit; the
daemon must be healthy after its restart before the repair is accepted.

Hosts still running 0.5.1 need the signed one-time
[0.5.1 → 0.6.0 recovery bridge](docs/RECOVER-0.5.1-TO-0.6.0.md) before normal
self-update can resume.
