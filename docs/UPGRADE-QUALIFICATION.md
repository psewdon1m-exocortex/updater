# Signed upgrade and rollback qualification — 2026-09-19

The protocol-2 candidate was exercised in an isolated Ubuntu 24.04 WSL2 host
with systemd as PID 1 and its own Docker engine. No production host was changed.
Baselines are actual published artifacts, verified against their original
RSA-PSS signatures and immutable image digests. They are not observations of
the operator's installed production versions.

| Component | Actual baselines | Target | Checks |
| --- | --- | --- | --- |
| Updater | 0.4.7, 0.4.8, 0.4.9 | candidate 0.5.0 | Signed install; actual candidate activation; forced systemd start failure; original binary hash restored; environment, registry and trust retained |
| Gryphon | 0.1.2, 0.1.3 | published 0.1.4 | Upgrade and automatic rollback; two connections/bindings retained; idempotent job replay |
| Neptune | 0.1.5, 0.1.6 | published 0.1.7 | Upgrade and automatic rollback; project schedule, parallelism and tokens retained |
| Saturn | 0.1.14, 0.1.15, 0.1.16 | candidate 0.2.0 | Activation, forced health failure, restoration of settings changed after activation, successful upgrade |

Candidate signatures use six independent disposable RSA-3072 keys held only in
the stand's tmpfs. Production private keys remain in GitHub Secrets. The new
candidate packages are not published or production-signed. After GitHub API
rate limiting, an isolated HTTPS catalog served cached production artifacts
unchanged and the separately signed candidates. Repository-coordinate lookup
uses a synthetic Kernel fixture; cross-service production discovery is outside
this qualification. Saturn's local candidate package tests the signed update
manifest/Compose path, not its complete release/SBOM/bootstrap publication job.

The trials exposed and fixed these compatibility issues:

- Released Gryphon health lacks its version; use its existing protected admin
  status socket to inspect the running process.
- Legacy Volt/Saturn health lacks a version. Only legacy releases below 0.2.0
  may prove their actual running image using the signed immutable digest.
  Wrong reported versions, tags, stopped containers and image mismatches fail.
- Old Laboratory cannot restore its own archive after an AI-settings migration.
  A signed manifest capability selects the candidate's offline restore CLI,
  with writers stopped, before restoring the old deployment.
- Saturn rollback needs its non-root API service's secret/storage mounts and
  a separate enabled database maintenance barrier. The candidate rollback CLI
  restores the old database without applying newer migrations.
- The first protocol-1 transition now has `updater migrate-head`: explicit root
  acknowledgement, original saved ZIP on stdin, and the same authenticated
  protocol-2 daemon, signature checks, rollback and durable job tracking.

Full Go vet/race tests pass for the isolated source candidate. Unit tests cover
receipt tampering, missing acknowledgement, interrupted input, legacy-version
image proof, recovery ordering and signed recovery capabilities. The stand also
verified an ordinary systemd/WSL restart: helpers start automatically and their
persistent settings remain. A normal reboot does not require archive restore.
If a reboot interrupts an update that needs data rollback, the original saved
operator ZIP is required because transient rollback bytes are intentionally in
RAM. WSL restart is not a physical power-loss qualification.

The complete per-service matrix and raw logs are retained in the operator's
local audit directory; published release qualification must identify its exact
CI-built digest separately.
