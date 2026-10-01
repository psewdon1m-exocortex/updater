#!/usr/bin/env python3
"""Prove the candidate archive is accepted by Updater 0.6.6's closed member set."""

import base64
import pathlib
import re
import subprocess
import sys
import tarfile

if len(sys.argv) != 3:
    raise SystemExit("usage: test-legacy-installer.py <install.tar.gz> <window-public.pem>")

archive_path = pathlib.Path(sys.argv[1])
public_path = pathlib.Path(sys.argv[2])
expected = {
    "updater/updater-linux-amd64": 64 * 1024 * 1024,
    "updater/install.sh": 256 * 1024,
    "updater/systemd/updater.service": 64 * 1024,
    "updater/release-trust/updater.pem": 16 * 1024,
    "updater/release-trust/neptune.pem": 16 * 1024,
    "updater/release-trust/gryphon.pem": 16 * 1024,
    "updater/release-trust/wyvern.pem": 16 * 1024,
}
directories = {"updater", "updater/systemd", "updater/release-trust"}
with tarfile.open(archive_path, "r:gz") as archive:
    members = archive.getmembers()
    actual = {member.name for member in members if member.isfile()}
    if actual != set(expected) or len(actual) != sum(member.isfile() for member in members):
        raise SystemExit("Candidate archive adds/removes a member rejected by Updater 0.6.6")
    if any(not member.isfile() and (not member.isdir() or member.name not in directories) for member in members):
        raise SystemExit("Candidate archive has an unsupported member type or directory")
    if any(member.size < 1 or member.size > expected[member.name] for member in members if member.isfile()):
        raise SystemExit("Candidate archive exceeds a legacy member limit")
    installer_file = archive.extractfile("updater/install.sh")
    if installer_file is None:
        raise SystemExit("Signed installer script is missing")
    installer = installer_file.read().decode("utf-8")

match = re.search(r"^window_public_b64='([A-Za-z0-9+/=]+)'$", installer, re.MULTILINE)
if not match or "__WINDOW_PUBLIC_KEY_BASE64__" in installer:
    raise SystemExit("Signed installer has no embedded Window trust anchor")
if base64.b64decode(match.group(1), validate=True) != public_path.read_bytes():
    raise SystemExit("Embedded Window trust anchor differs from the pinned public key")
subprocess.run(["sh", "-n"], input=installer, text=True, check=True)
print("0.6.6 archive format and embedded Window trust anchor verified")
