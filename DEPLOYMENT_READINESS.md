# updater deployment and recovery contract

A single host daemon serves typed authenticated operations. Connected heads can initialize Neptune; only Saturn consumes Gryphon in this profile and may receive the explicit host-recovery grant. Job reads are scoped to the requesting head. Missing required helpers are reconciled automatically after head registration and trusted Kernel configuration. One filesystem lock and durable job reservation serialize updates, installation, CLI actions, self-update and helper restore. Interrupted jobs become terminal failures with rollback snapshots retained. Self-update verifies the RSA-signed manifest, binary and installer archive before preparing host permissions/systemd, replacing the binary, and verifying its reported version; failure restores the prior binary and unit. Helper recovery encrypts configuration, bindings, journals and spools with a separately retained passphrase and uses a crash-recoverable directory transaction.

## Trust and operator prerequisites

The selected deployment profile contains Kernel, Volt, Saturn, Updater, Neptune and Gryphon. Per-host helpers are reused when healthy; attaching a service does not silently downgrade or reinstall them. Operator control is available through connected service Settings and typed CLI actions. Jobs retain their identifiers across page reloads and must reach a verified terminal result.

Release manifests use detached RSA-PSS-SHA256 signatures from a separately provisioned per-project RSA public key of at least 3072 bits. Use scripts/create-release-key.mjs to generate operator-owned signing material; never commit the private key. Provision the public trust key before deployment and publish matching signed releases before upgrading a real host. Saturn also retains its Ed25519 installer signature. The six-service head bundles require Updater 0.4.0 or newer.

Populate actual Kernel/Volt bootstrap coordinates, service tokens, SFTP host fingerprint, deployment CIDRs and release trust files. Secrets must not appear in links, responses, browser persistence or logs. Crawler directives supplement authenticated/private access; they do not hide public data from an uncooperative crawler. Resolve service data and generated link origins through Kernel; bootstrap trust and local loopback helper endpoints are explicit exceptions.

## Recovery boundaries

Keep the Access Key and helper-recovery passphrase separately from their archives. Main-service recovery retains user settings and application data while preserving or requiring re-enrollment of external host trust. The encrypted helper profile is controlled by Updater and contains Neptune/Gryphon state and credentials plus Updater job/rollback history. It excludes executable files, release trust keys, systemd units and head deployment environments. Install trusted software and register target heads before restoring. The bounded helper archive fails explicitly at 128 MiB expanded or 10000 files; it never silently omits data.

## Acceptance evidence

The seven-area policy in .github/pre-push-gate.json is required after native CI verification. Public indexing is intentionally not applicable. For an uncommitted local review run the gate with --worktree after the native checks. Gate PASS checks policy/evidence/verification linkage; it is not a substitute for executing the integration scenarios.

Qualify the connected system with real HTTP Kernel→Volt authentication, clean archives/restores, PostgreSQL and pinned SFTP, independent Volt mirror, Windows folder synchronization, network interruption/replay, signed artifact rejection, private-edge negative cases and helper installation/reuse. Record PASS, FAIL and NOT_RUN separately. Production credentials, signed publication and actual deployment remain operator provisioning operations.

See [README](README.md) for service commands.
