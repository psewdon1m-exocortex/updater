# Saved-copy group update protocol

The own-head API advertises `mastermind.saved-copy.v2` together with components, spool and enrollment capabilities. Group updates require one exact signed release containing Core, Runtime and Worker. Both the target manifest and the protected, digest-pinned installed manifest must contain `saved_copy_protocol: 2` in their `mastermind` section. The live Core writer-barrier receipt must also report `saved_copy_protocol: 2`. A legacy Core is rejected before component stop or replacement; it needs a separately qualified compatibility migration.

1. Verify and pre-pull all three immutable images before the application takes its writer barrier.
2. The application creates its standard encrypted ZIP and starts a single-use browser download from the original `Create backup and install` action. After the response transfer completes, it streams the transient server ZIP to the own-head spool and deletes the server copy. No second confirmation or file selection is required for the normal update. Browser download initiation does not prove local disk persistence.
3. The application checks the ZIP size and SHA-256 before the spool handoff. Neither the browser nor the API buffers the entire archive. Interrupted download or spool failure blocks installation.
4. Updater checks the sealed stream against the signed `exocortex.update-backup.v2` receipt, head, request, release, size and SHA-256 before accepting `/v2/updates`.
5. The existing durable job ID survives reconnects. An identical lost-response retry returns the job even after transient ZIP cleanup. A different request identity or receipt cannot reuse it.

Group spool bytes live only in a verified private tmpfs directory under `/dev/shm`; there is no disk fallback. The configured maximum is 8 GiB, but available tmpfs space and its reserve can impose a smaller limit. Insufficient space fails before installation. Deployment metadata lives separately under the durable Updater state directory. Terminal jobs delete their spool. A restart removes stale transient spools after the daemon acquires its listener; it does not delete installed application data.

`DELETE /v1/heads/{head}/backup-spools/{spool}` discards an own unclaimed preparation. It cannot discard another head's spool or an accepted job's recovery material. Interrupted uploads and cancelled preparations use this endpoint; an outage falls back to the bounded transient TTL.

An interrupted mutated job can recover from the exact original saved ZIP through `POST /v2/jobs/{id}/rollback-saved-spool` with `{ "spool_id": "...", "operator_saved": true }`. This route authenticates the job's head and checks its original checksum. The application exposes it through its authenticated saved-backup route; the browser never receives an administrative socket. A completed upgrade uses a fresh saved backup and writer barrier when returning to a previous version, preserving current data.

A normal host reboot starts the installed services with their persistent volumes. Operator-copy recovery is required only for an interrupted update whose recovery needs ZIP bytes no longer present in tmpfs. Never claim host-level N−1/N−2 compatibility from mocked Compose tests: qualify actual signed images on Linux/systemd before release promotion.
