# updater deployment and recovery contract

This service-local record is subordinate to the coordinated
[Part 11 deployment profile](../.docs/PART_11_INITIAL_MULTI_SERVICE_DEPLOYMENT.md)
and the shared-agent contracts in
[Part 09](../.docs/PART_09_SERVICE_AGENTS_DEPLOYMENT_AND_LIFECYCLE.md) and
[Part 10](../.docs/PART_10_SERVICE_AGENTS_UI_AND_OPERATOR_WORKFLOWS.md).

A single host daemon serves typed authenticated operations. Connected heads can initialize Neptune; only Saturn consumes Gryphon in this profile and may receive the explicit host-recovery grant. Job reads are scoped to the requesting head. Missing required helpers are reconciled automatically after head registration and trusted Kernel configuration. One filesystem lock and durable job reservation serialize updates, installation, CLI actions, self-update and helper restore. Interrupted jobs become terminal failures with rollback snapshots retained. Self-update verifies the RSA-signed manifest, binary and installer archive before preparing host permissions/systemd, replacing the binary, and verifying its reported version; failure restores the prior binary and unit. Helper recovery encrypts configuration, bindings, journals and spools with a separately retained passphrase and uses a crash-recoverable directory transaction.

## Trust and operator prerequisites

The selected deployment profile contains Kernel, Volt, Saturn, Updater, Neptune and Gryphon. Per-host helpers are reused when healthy; attaching a service does not silently downgrade or reinstall them. Operator control is available through connected service Settings and typed CLI actions. Jobs retain their identifiers across page reloads and must reach a verified terminal result.

Release manifests use detached RSA-PSS-SHA256 signatures with a per-project RSA key of at least 3072 bits. Keep Updater's private key only in GitHub Secrets and expose it only to the protected release-signing job. CI derives the public counterpart and embeds it in Updater's versioned `bootstrap.sh`; bootstrap creates `/etc/exocortex/release-trust/updater.pem`, verifies the manifest before downloading binaries or packages, and never replaces an existing mismatching key automatically. No `scp`, manual release-key fingerprint or separately downloaded public key is part of this trust path. Saturn also retains its Ed25519 installer signature. The six-service head bundles require Updater 0.4.4 or newer.

Populate actual Kernel/Volt bootstrap coordinates, service tokens, SFTP host fingerprint and exact trusted server-proxy hops. Updater has its own versioned bootstrap and mode-`0600` `/etc/exocortex/updater/.env`; registered heads retain separate environment files. Browser-facing head login routes use the public-authenticated model and must not acquire `OPERATOR_CIDR`, a VPN prerequisite or an operator IP allow-list. Secrets must not appear in links, responses, browser persistence or logs. Crawler directives supplement authenticated access; they do not hide public data from an uncooperative crawler. Resolve service data and generated link origins through Kernel; bootstrap trust and local loopback helper endpoints are explicit exceptions.

## Recovery boundaries

Keep the Access Key and helper-recovery passphrase separately from their archives. Main-service recovery retains user settings and application data while preserving or requiring re-enrollment of external host trust. The encrypted helper profile is controlled by Updater and contains Neptune/Gryphon state and credentials plus Updater job/rollback history. It excludes executable files, release trust keys, systemd units and head deployment environments. Install trusted software and register target heads before restoring. The bounded helper archive fails explicitly at 128 MiB expanded or 10000 files; it never silently omits data.

## Acceptance evidence

The seven-area policy in .github/pre-push-gate.json is required after native CI verification. Public indexing is intentionally not applicable. For an uncommitted local review run the gate with --worktree after the native checks. Gate PASS checks policy/evidence/verification linkage; it is not a substitute for executing the integration scenarios.

Qualify the connected system with real HTTP Kernel→Volt authentication, clean archives/restores, PostgreSQL and pinned SFTP, independent Volt mirror, Windows folder synchronization, network interruption/replay, signed artifact rejection, private-edge negative cases and helper installation/reuse. Record PASS, FAIL and NOT_RUN separately. Production credentials, signed publication and actual deployment remain operator provisioning operations.

See [README](README.md) for service commands.
