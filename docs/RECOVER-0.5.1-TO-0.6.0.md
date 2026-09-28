# Recovering the 0.5.1 → 0.6.0 self-update

Updater 0.5.1 rejects the signed 0.6.0 installer with `invalid signed installer
archive member`: the archive adds `release-trust/wyvern.pem`, while the 0.5.1
extractor accepts exactly the six older files. The rejection happens before
the daemon binary is replaced.

For a host still running 0.5.1, transfer
[`scripts/recover-0.5.1-to-0.6.0.sh`](../scripts/recover-0.5.1-to-0.6.0.sh)
to the host and run it as root from a separate SSH shell:

```sh
bash recover-0.5.1-to-0.6.0.sh
updater version
updater status
```

The script accepts only the published 0.6.0 release. It verifies the manifest
signature against the host's pinned Updater release key, checks the installer's
digest and exact members, verifies its binary, saves the previous binary and
systemd unit, then installs and waits up to 30 seconds for 0.6.0 to report its
version through the health socket. It attempts to restore the
previous binary and unit if installation or health verification fails, retaining
the backups for operator repair if the rollback fails. The old failed
job remains in history; a successful `updater status` and version 0.6.0 are
the result of the recovery.
