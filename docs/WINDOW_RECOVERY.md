# Window host recovery

Status: implemented and tested locally for Updater 0.6.15; release publication
and production deployment qualification are separate operator decisions. Part
03, the approved enrollment decision and Window's documented identity recovery
contract govern this extension.

The recovery point is the last successfully committed manual archive. No
automatic schedule is added. The restore target is at most three minutes after
the trusted installation, archive and independent recovery key are available;
this is a rehearsal target, not a production availability guarantee.

## Scope and replacement

One independently encrypted `window` scoped v2 `.exorecovery` contains exactly
`var/lib/window-ssh/.ssh/authorized_keys`. Its only accepted nonempty value is
Window's canonical Ed25519 public key with the fixed
`restrict,command="/usr/local/bin/window mcp"` prefix and `window-client` comment.
Missing pairing is exported as an explicit empty member. Restoring it removes
a later pairing. Only this root-owned public identity is replaced.

Grants, leases, live output, audit/test results, SSH operator credentials and
passwords, the PC's private key, server SSH host keys, binaries and systemd units
are excluded. Provision accounts, binary, unit and SSH access through the trusted
installer first. The public pairing file is root-owned mode 0644 inside a
root-owned mode 0755 directory so the dedicated MCP account can read it without
being able to change it.

The same transaction journal, fsync, staging, verified rollback and interrupted
restore repair used by other host services apply. Unknown files, injected SSH
options, extra keys, symbolic links, wrong service/key, tampering and missing
identity members fail before live mutation. A successful restore requires the
real Window API to report the expected pairing fingerprint, closed access,
no live session and no lease. Restart invalidates existing grants, including
after export or a failed restore. Window recovery does not recreate application
containers or alter neighboring service files.

An archived pairing can name a formerly paired PC. Confirm the replacement
in the root TUI, review the restored identity and explicitly open any new grant.
Re-pair if that PC is no longer trusted. The pipeline's Saturn producer token
is separate from both the pairing and recovery key. Saturn revoke blocks future
uploads; it does not delete previously committed recovery archives or revoke
the diagnostic grant. Window's own Revoke action closes that grant.

## Operator workflow and compatibility

1. Saturn Synchronization: choose `Window recovery`, enter the host name, create
   its one-time setup code. Storage is `backups/window/<host>/...`.
2. Root SSH session: `sudo updater tui` → Window → Configure recovery storage.
   Saturn discovery uses Updater's own Kernel machine connection and the existing
   fail-closed discovery/fallback rules.
3. Export Window's independent 256-bit recovery key to a new private file, then
   copy that file off the host. Do not paste it into Register or logs.
4. Create Window recovery archive and confirm `CREATE WINDOW RECOVERY`.
5. Restore from a downloaded `.exorecovery` and its private key file, confirm
   `RESTORE WINDOW`. Review the paired identity; access stays closed.

`windowops` and its restricted Window-only TUI receive no archive-import or
key-export capability. Existing service-scoped API listeners receive none.
The scope uses the existing encrypted format and generic Saturn backup-ingest
API, so no database migration, new daemon or Neptune update is required. The
Updater binary must include this extension; older Updater rejects the new
`window` scope. Old combined archives with no Window member preserve a
separately installed Window. Existing other scopes and keys remain valid.
Legacy combined exports and publication retain their four-scope contract and
never require a new Window identity or token. Window always uses its independent
scoped pipeline.

## Local verification

Ordinary Updater tests cover encryption, key separation, resumable publication,
receipt checks, rejection before mutation, sibling isolation, successful
restore, health-failure rollback, interrupted restore and restricted TUI/API.
Saturn tests cover creation, encrypted Window namespace ingestion and the
existing revoke workflow.

The explicit `window_integration` Go build tag adds a fresh disposable Linux
container rehearsal with the actual Window broker. It exercises pairing,
open/live grants, export, paired/unpaired restore, old-lease rejection and
readability as the dedicated SSH account. The rehearsal also injects startup
failure and interrupts restore
after directory replacement, then verifies rollback for both an originally
running and an originally inactive broker without touching neighboring services.
Only systemd's start/stop lifecycle is simulated; no production containers or
files are used. Run this tag with
`WINDOW_RECOVERY_CONTAINER_TEST=1` and `WINDOW_SOURCE` pointing at the Window
checkout, in a fresh Linux container with root and the trusted Go toolchain.
It refuses existing service state. Real systemd/OpenSSH and external Saturn
transport remain part of future deployment qualification.
