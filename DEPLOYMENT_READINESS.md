# updater deployment and recovery contract

## Kernel-dependent host recovery discovery

The normal recovery form already contains only a service setup code.
`ResolveOrigin` obtains `services.saturn.sni` and `services.saturn.port` with
Updater's own Kernel machine identity. A Kernel release with the scoped
bootstrap-grant correction is required for unchanged historical host principals;
an additional Updater runtime release is not required for that correction.
The tagged `internal/hostrecovery/kernel_http_integration_test.go` test runs
the real Go discovery client against Kernel's actual HTTP server and Volt HTTP
client. Run Kernel's `tests/updater-recovery-http.test.js` with
`UPDATER_HTTP_TEST_SOURCE` pointing to this checkout to verify old-grant denial,
migrated discovery and persistent revocation for all five scopes. A stale saved
Saturn address neither overrides valid discovery nor silently bypasses HTTP 403.

## 0.6.16 host recovery HTTP correction

All five scopes share the same publisher. Its PATCH requests now explicitly
declare `Content-Type: application/offset+octet-stream`; missing content type
caused Saturn HTTP 415 after successful enrollment on Updater 0.6.15.
The strict transport regression rejects a missing header. The disposable Saturn
HTTP fixture also verifies missing/unsupported media types accept no bytes,
resume offsets, namespace isolation and real local filesystem commit.
The tagged Go test in `internal/hostrecovery/saturn_http_integration_test.go`
exercises production enrollment, private credential persistence, capabilities,
publication, completed replay, download, SHA-256, decryption and isolated restore
against the actual Nest/Fastify server for all five scopes.

Run the cross-service check from the Saturn checkout with
`UPDATER_HTTP_TEST_SOURCE` set to the absolute Updater checkout path:
`pnpm --filter @saturn/api exec vitest run src/host-recovery-http.integration.test.ts --reporter=verbose`.
It requires local Docker and the Go test image/cache. The fixture uses synthetic
credentials and real filesystem storage. Metadata is in-memory by default;
setting `SATURN_HOST_RECOVERY_TEST_DATABASE_URL` to a disposable local database
selects the real PostgreSQL repository in a unique migrated schema, removed
after the test. Both modes passed locally for all five scopes. Production SFTP,
systemd lifecycle and the production host remain unqualified by this fixture.
Release publication does not by itself qualify production SFTP, systemd or the
production host. Existing managed keys and Saturn enrollment remain valid;
reconnecting does not repair a 415.

## 0.6.15 Window identity recovery

Updater 0.6.15 gives Window an independent root-operated Saturn recovery
identity, managed encryption key and scoped archive. Only the canonical public
MCP pairing is restored, including an explicitly unpaired snapshot. Grant,
lease and live state remain ephemeral; verification requires the expected
fingerprint and closed access. The restricted `windowops` TUI cannot import
archives or export keys. Legacy combined exports retain their original
four-scope contract.

Local qualification includes all five host scopes, malformed payload rejection,
resumable transport/receipt checks, sibling isolation, rollback and interrupted
restore, plus the real Window broker in a disposable Linux container. That
rehearsal simulates systemd lifecycle; actual systemd/OpenSSH and an external
Saturn deployment are still `NOT_RUN` for this extension. See
[Window recovery](docs/WINDOW_RECOVERY.md). Release publication does not by
itself declare the production deployment qualified.

## 0.6.14 host operation recovery and Window diagnostics

Updater 0.6.14 preserves completed and failed host-owned operation history in
both scoped and legacy recovery archives. Restore accepts only the explicit
Updater, Neptune, Gryphon and Wyvern operation-service catalog, validates job
filenames and keeps damaged-job errors free of stored job contents. Recovery
coverage verifies resumed uploads, remote receipt identity, independent
per-service keys, service isolation and rollback after failed health checks.

The installed `windowops` account may also invoke grant-gated, read-only
`window diagnose` commands through its narrow sudo policy. The restricted TUI
documents the same boundary; it does not grant mutation or unrestricted Window
access.

## 0.6.13 scoped host recovery

Updater 0.6.13 adds service-isolated remote recovery archives for Updater,
Neptune, Gryphon and Wyvern. Saturn is resolved through Updater's own Kernel
machine principal, while per-service producer credentials and managed recovery
keys remain in private files owned by the Updater service user. The root
operator API and TUI expose explicit configuration, archive, restore and key
export workflows; existing passphrase archives remain restorable through the
legacy path.

## 0.6.12 restricted Window flag

Updater 0.6.12 exposes the restricted Window operator console as
`updater tui -window` and provisions the matching narrow `windowops` sudoers
entry. Window releases that require this spelling must pin Updater 0.6.12 or
newer.

## 0.6.11 image retention and restricted Window operations

Updater 0.6.11 adds a bounded Docker image cleanup operation on the private
root operator socket. It preserves running images, the immediately previous
deployment generation, protected job references and the two newest eligible
images per registered repository. Cleanup requires a fresh preview and a
separate confirmation. Previous immutable image references are retained outside
bounded job history so rollback can verify or pull the exact historical image
before changing the current deployment.

The installer also provisions the unprivileged `windowops` account and a narrow
sudoers rule for `updater tui -window`, Window pairing and bounded test
capture. Existing credentials are preserved; a new account remains locked until
an operator installs an SSH key or sets a password.

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

A single host daemon serves typed authenticated operations. Connected heads can initialize Neptune; Saturn and Chronos consume Gryphon, but shared-host recovery is available only through the root operator socket and `sudo updater tui`. Job reads are scoped to the requesting head. Missing required helpers are reconciled automatically after head registration and trusted Kernel configuration. One filesystem lock and durable job reservation serialize updates, installation, CLI actions, self-update and helper restore. Interrupted head updates retain recovery metadata; their ZIP is held only in RAM/tmpfs, so recovery after a process restart uses the operator's downloaded copy. Ordinary host restarts retain application data and require no archive restore. Self-update verifies the RSA-signed manifest, binary and installer archive before preparing host permissions/systemd, replacing the binary, and verifying its reported version; failure restores the prior binary and unit. Helper recovery encrypts configuration, bindings, journals and spools with a separately retained passphrase and uses a crash-recoverable directory transaction.

## Host recovery upload resilience — 2026-10-11

The 0.6.16 media-type correction succeeded in production: Window observed
`component-a288ef1e38208a2bf8cd4cb8a8f8cab0` as COMPLETED at 13:28:38 UTC.
Updater 0.6.17 adds the following resilience contract on top of that verified
media-type correction.

The shared publisher now makes at most three attempts for transient HTTP/network
failures. It reuses the reservation idempotency key, reconciles a lost chunk
response through HEAD, and repeats completion without replacing committed
bytes. Permanent 4xx errors terminate; an offset conflict is reconciled only
within the exact attempted chunk. Backoff respects the existing operation
deadline. When publication returns a terminal failure, Updater attempts to cancel
only its own unfinished Saturn run with an independent five-second context.
Cancellation failure remains visible alongside the original error; the Saturn
inactivity TTL is the fallback for an unreachable server or interrupted process.

Deploy the Saturn API's scoped producer DELETE and incomplete-run maintenance
before this Updater change. Keep existing producer identities and recovery keys.
An old API can return 404 to cancellation; completed archives remain protected.
No production credentials, host commands, release publication or deployment
are needed for the disposable HTTP regressions.

Tests inject permanent failure and cancellation at publication stages, temporary
502/429, exhausted 503 retries, and lost create/chunk/complete responses.
The tagged Saturn HTTP test also loses all three acknowledgements against the
actual Nest/Fastify API, PostgreSQL and filesystem, then downloads, verifies,
decrypts and restores all five host-service scopes. SFTP and actual host crash
rehearsals remain separate production acceptance checks.

## Trust and operator prerequisites

The selected deployment profile contains Kernel, Volt, Saturn, Updater, Neptune and Gryphon. Per-host agents are reused when healthy; attaching a service does not silently downgrade or reinstall them. Root `sudo updater tui` owns shared-agent release checks and updates. Each service's Settings owns its own Neptune policy and scoped agent bindings. Jobs retain their identifiers across page reloads and must reach a verified terminal result.

Release manifests use detached RSA-PSS-SHA256 signatures with a per-project RSA key of at least 3072 bits. Keep Updater's private key only in GitHub Secrets and expose it only to the protected release-signing job. CI derives the public counterpart and embeds it in Updater's versioned `bootstrap.sh`; bootstrap creates `/etc/exocortex/release-trust/updater.pem`, verifies the manifest before downloading binaries or packages, and never replaces an existing mismatching key automatically. No `scp`, manual release-key fingerprint or separately downloaded public key is part of this trust path. Saturn also retains its Ed25519 installer signature. The six-service profile requires Updater 0.4.5 or newer for canonical Gryphon and unified Neptune release discovery.

Populate actual Kernel/Volt bootstrap coordinates, service tokens, SFTP host fingerprint and exact trusted server-proxy hops. Updater has its own versioned bootstrap and mode-`0600` `/etc/exocortex/updater/.env`; registered heads retain separate environment files. Browser-facing head login routes use the public-authenticated model and must not acquire `OPERATOR_CIDR`, a VPN prerequisite or an operator IP allow-list. Secrets must not appear in links, responses, browser persistence or logs. Crawler directives supplement authenticated access; they do not hide public data from an uncooperative crawler. Resolve service data and generated link origins through Kernel; bootstrap trust and local loopback helper endpoints are explicit exceptions.

## Recovery boundaries

Keep the Access Key and helper-recovery passphrase separately from their archives. Main-service recovery retains user settings and application data while preserving or requiring re-enrollment of external host trust. Updater, Neptune, Gryphon and Wyvern each have an optional, independently enrolled and operated recovery pipeline; a host needs identities only for services actually installed there. Each producer token can write only through Saturn Gateway into its derived `backups/<service>` namespace and receives neither Storage Box credentials nor neighboring read access. Creating an archive quiesces only the selected service. Each archive excludes executable files, release trust keys, systemd units and head deployment environments. Install the matching trusted software and register target heads before restoring a downloaded local archive. A scoped restore stages, verifies and rolls back only the selected service roots. Each bounded archive fails explicitly at 128 MiB expanded or 10000 files; it never silently omits data. Legacy combined v1 archives remain a root-CLI migration input but are no longer created by the TUI.

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
