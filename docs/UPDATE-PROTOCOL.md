# Update protocol 2 — release preparation

This checkout requires the signed **Updater 0.5.0** package. It has not been
published by these changes. Publish and verify that dependency before building
service releases; the bundle pin and minimum updater version must agree.

## Operator flow

Open Check for updates. Discovery runs immediately; Check again repeats it.
Install X opens the backup warning. The standard full ZIP is created once,
downloaded and returned unchanged with a short-lived signed receipt. Install
remains blocked until saving completes (File System Access API) or the operator
explicitly confirms that the browser download is saved. A failed or cancelled
save never starts installation. The ZIP limit is 128 MiB; an oversized export
fails before any release mutation.

Updater, Neptune and consumed Gryphon use the same dialog without a backup step.
The panel tracks durable jobs, actual state/error, and indeterminate progress when
no measured total is available. Reload/restart reconnects by job/request ID and
server job history. Completion triggers a fresh version check. Helpers installed
on one host remain shared between its registered heads.

## Archive lifecycle and recovery

No pre-update ZIP is retained on the application host. A downloaded archive is
bound to service, head, exact version, request ID, SHA-256, size and expiry
(15 minutes) by HMAC with the local head control token. This token never enters
the browser. Updater independently verifies the receipt, signed release, digests,
minimum version and actual running version. Downgrade and reinstall are rejected.

During an update, only RAM holds the ZIP for automatic rollback. When a restore
tool needs a path, Updater uses verified tmpfs and removes the temporary file.
After daemon/host restart, use Rollback and upload the original saved ZIP; its
checksum must match the job. Without that operator copy, automatic recovery after
restart is unavailable. Keep the ZIP until the release is accepted. The other
permitted durable backup destination is Saturn through Neptune automatic backup.
Job/deployment metadata is retained, not ZIP contents or copied .env secrets.

## First transition from protocol 1

Publish and verify signed Updater 0.5.0 first; build the new head release with
that pinned bundle. Existing release tags and trust anchors stay immutable.

1. Download the old head's normal full ZIP to the operator PC and confirm it is
   saved. Keep this original copy until the new release is accepted.
2. On the host run `sudo updater update --head <registered-head-id>`. This uses
   the existing trust pin, verifies the signed Updater release and preserves the
   registry, environment and installed helper state.
3. Stream the saved ZIP from the operator PC to the root migration command, for
   example (replace the host, head and exact published version):

   `ssh root@host 'updater migrate-head --head chronos --version 0.2.0 --saved-backup-stdin --confirm-saved' < chronos-backup.zip`

   The command requires root and explicit saved-copy confirmation. It reads at
   most 128 MiB in memory, binds the original bytes to the head and exact release,
   and submits the normal authenticated protocol-2 request through the local
   Unix socket. The daemon verifies the receipt, release signature and image
   digest and performs its standard update/rollback. It prints a job ID;
   `sudo updater jobs` reports progress. A successful submission is not a
   completed upgrade. No ZIP is written to the host filesystem by this command.
4. Check the completed job and actual service health/version. Open Updates in the
   new UI. Subsequent updates use that UI and its mandatory saved-copy step.

Do not rerun a fresh-install head bootstrap over an existing installation; those
scripts deliberately reject it. The old direct apply API cannot carry the saved
copy receipt. Neither route is the first-migration procedure.

Ordinary host restart preserves installed applications, databases and settings;
it does not require restoring services from ZIPs. If a host/daemon restart
interrupts an update that needs data rollback, its in-memory ZIP is gone. Supply
the original saved ZIP for that job's checksum-verified recovery.

## Verification boundaries

Regression coverage includes forged/expired/wrong-target receipts, altered ZIPs,
missing acknowledgement, exact stable selection, real-version health checks,
job recovery, rollback and legacy archive migration. Browser checks use actual
service CSS with synthetic release/ZIP/job endpoints. A signed release rehearsal
on a disposable Linux/systemd host is still required by the production release
gate; unit and browser fixture success do not certify a live deployment.
