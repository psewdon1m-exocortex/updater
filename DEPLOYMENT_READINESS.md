# updater deployment and recovery contract

## 0.6.10 scoped Neptune unlink

Updater 0.6.10 adds the authenticated service-owned Neptune unlink lifecycle.
It disables local schedules before disconnecting the remote binding, removes
the scoped project state only after the disconnect succeeds, and retains the
durable job outcome for operator review.

## 0.6.9 Window first-install source

On a host with an existing Kernel connection whose Register has no
`repositories.window.url`, Window 0.0.1's exact-version bootstrap seeds the
Window repository, but Updater 0.6.8 rejects the ordinary source lookup before
installing Window. Updater 0.6.9 gives only the root-only
`updater window install --version 0.0.1` path the seeded repository. It verifies
the pinned Window release signature, exact tag, manifest and asset digests as
usual. TUI release discovery and updates still require a Window Register entry
or an unavailable Kernel connection with an operator-configured fallback. Do
not force a Kernel outage to bypass a missing Register key. On the affected
host, update Updater to 0.6.9 through its verified self-update path, rerun the
same immutable Window bootstrap, and verify the running Window unit and closed
read grant. This host rehearsal is still `NOT_RUN` until observed on the server.

## 0.6.8 Window trust transition

Updater 0.6.6 accepts only its original seven signed installer files. The
0.6.8 archive preserves that exact member set and embeds Window's public key
inside its signed `install.sh`. The release workflow checks the archive against
the 0.6.6 member limits before signing and publishing. On a representative
host, update from 0.6.6 to 0.6.8, then confirm that
`/etc/exocortex/release-trust/window.pem` matches the public key in the signed
archive, the new daemon reports 0.6.8, and Window is initially uninstalled.
Exercise a failed health activation and Updater repair before calling that host
deployment ready. Until this host rehearsal is recorded, production deployment
readiness is `NOT_RUN`; it is separate from the immutable release checks.

This service-local record is subordinate to the coordinated
[Part 11 deployment profile](https://github.com/psewdon1m-exocortex/general/blob/main/PART_11_INITIAL_MULTI_SERVICE_DEPLOYMENT.md)
and the shared-agent contracts in
[Part 09](https://github.com/psewdon1m-exocortex/general/blob/main/PART_09_SERVICE_AGENTS_DEPLOYMENT_AND_LIFECYCLE.md) and
[Part 10](https://github.com/psewdon1m-exocortex/general/blob/main/PART_10_SERVICE_AGENTS_UI_AND_OPERATOR_WORKFLOWS.md).

A single host daemon serves typed authenticated operations. Connected heads can initialize Neptune; Saturn and Chronos consume Gryphon, and Saturn may receive the explicit host-recovery grant. Job reads are scoped to the requesting head. Missing required helpers are reconciled automatically after head registration and trusted Kernel configuration. One filesystem lock and durable job reservation serialize updates, installation, CLI actions, self-update and helper restore. Interrupted head updates retain recovery metadata; their ZIP is held only in RAM/tmpfs, so recovery after a process restart uses the operator's downloaded copy. Ordinary host restarts retain application data and require no archive restore. Self-update verifies the RSA-signed manifest, binary and installer archive before preparing host permissions/systemd, replacing the binary, and verifying its reported version; failure restores the prior binary and unit. Helper recovery encrypts configuration, bindings, journals and spools with a separately retained passphrase and uses a crash-recoverable directory transaction.

## Trust and operator prerequisites

The selected deployment profile contains Kernel, Volt, Saturn, Updater, Neptune and Gryphon. Per-host agents are reused when healthy; attaching a service does not silently downgrade or reinstall them. Root `sudo updater tui` owns shared-agent release checks and updates. Each service's Settings owns its own Neptune policy and scoped agent bindings. Jobs retain their identifiers across page reloads and must reach a verified terminal result.

Release manifests use detached RSA-PSS-SHA256 signatures with a per-project RSA key of at least 3072 bits. Keep Updater's private key only in GitHub Secrets and expose it only to the protected release-signing job. CI derives the public counterpart and embeds it in Updater's versioned `bootstrap.sh`; bootstrap creates `/etc/exocortex/release-trust/updater.pem`, verifies the manifest before downloading binaries or packages, and never replaces an existing mismatching key automatically. No `scp`, manual release-key fingerprint or separately downloaded public key is part of this trust path. Saturn also retains its Ed25519 installer signature. The six-service profile requires Updater 0.4.5 or newer for canonical Gryphon and unified Neptune release discovery.

Populate actual Kernel/Volt bootstrap coordinates, service tokens, SFTP host fingerprint and exact trusted server-proxy hops. Updater has its own versioned bootstrap and mode-`0600` `/etc/exocortex/updater/.env`; registered heads retain separate environment files. Browser-facing head login routes use the public-authenticated model and must not acquire `OPERATOR_CIDR`, a VPN prerequisite or an operator IP allow-list. Secrets must not appear in links, responses, browser persistence or logs. Crawler directives supplement authenticated access; they do not hide public data from an uncooperative crawler. Resolve service data and generated link origins through Kernel; bootstrap trust and local loopback helper endpoints are explicit exceptions.

## Recovery boundaries

Keep the Access Key and helper-recovery passphrase separately from their archives. Main-service recovery retains user settings and application data while preserving or requiring re-enrollment of external host trust. The encrypted helper profile is controlled by Updater and contains Neptune/Gryphon state and credentials plus Updater job/rollback history. It excludes executable files, release trust keys, systemd units and head deployment environments. Install trusted software and register target heads before restoring. The bounded helper archive fails explicitly at 128 MiB expanded or 10000 files; it never silently omits data.

## Acceptance evidence

The seven-area policy in .github/pre-push-gate.json is required after native CI verification. Public indexing is intentionally not applicable. For an uncommitted local review run the gate with --worktree after the native checks. Gate PASS checks policy/evidence/verification linkage; it is not a substitute for executing the integration scenarios.

Qualify the connected system with real HTTP Kernel→Volt authentication, clean archives/restores, PostgreSQL and pinned SFTP, independent Volt mirror, Windows folder synchronization, network interruption/replay, signed artifact rejection, private-edge negative cases and helper installation/reuse. Record PASS, FAIL and NOT_RUN separately. Production credentials, signed publication and actual deployment remain operator provisioning operations.

See [README](README.md) for service commands.

## Terminal operator entry point

`sudo updater tui` uses the matching daemon's private operator socket at
`/run/exocortex-admin/updater.sock`. The systemd unit must provide the separate
runtime directory. The listener requires root peer credentials and mode `0600`;
service-mounted sockets do not expose its routes. The terminal shows bounded
status and job metadata, masks credential input and reuses the signed helper
update/install paths. Closing SSH does not cancel an accepted operation.

The built-in Help view and [terminal console contract](docs/TUI.md) document
navigation, configuration prerequisites, reconnect and recovery. CI exercises
the UI through a real PTY and tests root/non-root socket access. Real Termius
desktop/mobile and live service enrollment remain separate acceptance checks.
