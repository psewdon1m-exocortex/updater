#!/usr/bin/env sh
set -eu
umask 077

repository="psewdon1m-exocortex/updater"
version="__UPDATER_BOOTSTRAP_RELEASE_VERSION__"
embedded_public_key_b64="__UPDATER_BOOTSTRAP_PUBLIC_KEY_BASE64__"
trust_file="${EXOCORTEX_RELEASE_TRUST_FILE:-/etc/exocortex/release-trust/updater.pem}"
max_manifest_bytes=2097152
max_installer_bytes=134217728

fail() {
  printf '%s\n' "updater bootstrap: $*" >&2
  exit 1
}

[ "$(id -u)" -eq 0 ] || fail "run as root"
printf '%s' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' || fail "invalid embedded release version"
[ ! -e /usr/local/lib/updater/updater ] && [ ! -e /etc/systemd/system/updater.service ] || fail "Updater is already installed; use 'updater update'"

if command -v apt-get >/dev/null 2>&1; then
  apt-get update
  DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates curl openssl python3 tar util-linux
fi
for command in curl openssl python3 tar flock; do
  command -v "$command" >/dev/null 2>&1 || fail "$command is required"
done

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT HUP INT TERM
printf '%s' "$embedded_public_key_b64" | openssl base64 -d -A >"$work/updater.pem" || fail "embedded release key is invalid"
openssl pkey -pubin -in "$work/updater.pem" -noout >/dev/null 2>&1 || fail "embedded release key is invalid"
if [ -e "$trust_file" ]; then
  if [ ! -f "$trust_file" ] || [ -L "$trust_file" ] || ! cmp -s "$work/updater.pem" "$trust_file"; then
    fail "installed Updater release key differs from this release"
  fi
fi

base="https://github.com/$repository/releases/download/updater-v$version"
manifest="$work/updater-release.json"
signature="$work/updater-release.json.sig.json"
curl -fsSL --proto '=https' --proto-redir '=https' --retry 3 --connect-timeout 10 --max-time 120 --max-filesize "$max_manifest_bytes" "$base/updater-release.json" -o "$manifest"
curl -fsSL --proto '=https' --proto-redir '=https' --retry 3 --connect-timeout 10 --max-time 120 --max-filesize 16384 "$base/updater-release.json.sig.json" -o "$signature"
python3 - "$manifest" "$signature" "$work/updater.pem" "$version" "$base" <<'PYVERIFY'
import base64, hashlib, json, pathlib, re, subprocess, sys, tempfile
from urllib.parse import urlparse
manifest, envelope, trust = map(pathlib.Path, sys.argv[1:4])
version, base = sys.argv[4:]
data = json.loads(manifest.read_text(encoding="utf8"))
signed = json.loads(envelope.read_text(encoding="utf8"))
if data.get("schema_version") != 1 or data.get("service") != "updater" or data.get("version") != version:
    raise SystemExit("Updater release identity mismatch")
if signed.get("schema") != "exocortex.release-signature.v1" or signed.get("algorithm") != "RSA-PSS-SHA256":
    raise SystemExit("Updater release signature envelope is invalid")
public = subprocess.run(["openssl", "pkey", "-pubin", "-in", str(trust), "-outform", "DER"], check=True, capture_output=True).stdout
if hashlib.sha256(public).hexdigest() != signed.get("key_id"):
    raise SystemExit("Updater release signer is not trusted")
description = subprocess.run(["openssl", "rsa", "-pubin", "-in", str(trust), "-text", "-noout"], check=True, capture_output=True, text=True).stdout
bits = re.search(r"Public-Key: \((\d+) bit\)", description)
if not bits or int(bits[1]) < 3072:
    raise SystemExit("Updater release trust requires RSA-3072")
with tempfile.TemporaryDirectory(prefix="updater-signature-") as directory:
    value = pathlib.Path(directory) / "signature.bin"
    value.write_bytes(base64.b64decode(signed.get("signature", ""), validate=True))
    subprocess.run(["openssl", "dgst", "-sha256", "-verify", str(trust), "-signature", str(value), "-sigopt", "rsa_padding_mode:pss", "-sigopt", "rsa_pss_saltlen:32", str(manifest)], check=True)
installer = data.get("installer") or {}
expected_url = f"{base}/updater-{version}-install.tar.gz"
if installer.get("url") != expected_url or not re.fullmatch(r"[a-f0-9]{64}", str(installer.get("sha256") or "")):
    raise SystemExit("Updater installer identity is invalid")
print(installer["url"])
print(installer["sha256"])
PYVERIFY

fields=$(python3 -c "import json; d=json.load(open('$manifest')); print(d['installer']['url']); print(d['installer']['sha256'])")
installer_url=$(printf '%s\n' "$fields" | sed -n '1p')
installer_sha=$(printf '%s\n' "$fields" | sed -n '2p')
archive="$work/updater.tar.gz"
curl -fsSL --proto '=https' --proto-redir '=https' --retry 3 --connect-timeout 10 --max-time 300 --max-filesize "$max_installer_bytes" "$installer_url" -o "$archive"
actual_sha=$(openssl dgst -sha256 "$archive" | awk '{print $NF}')
[ "$actual_sha" = "$installer_sha" ] || fail "Updater installer digest mismatch"

stage="$work/stage"
python3 - "$archive" "$stage" <<'PYARCHIVE'
import pathlib, tarfile, sys
archive, target = map(pathlib.Path, sys.argv[1:])
expected = {
    "updater/install.sh", "updater/updater-linux-amd64",
    "updater/systemd/updater.service", "updater/release-trust/updater.pem",
    "updater/release-trust/neptune.pem", "updater/release-trust/gryphon.pem",
}
target.mkdir(mode=0o700)
seen = set()
with tarfile.open(archive, "r:gz") as source:
    for member in source:
        name = member.name.rstrip("/")
        path = pathlib.PurePosixPath(name)
        if path.is_absolute() or ".." in path.parts or "\\" in name or member.issym() or member.islnk():
            raise SystemExit("unsafe Updater installer member")
        if member.isdir():
            continue
        if not member.isfile() or name not in expected or name in seen:
            raise SystemExit("unexpected Updater installer member")
        seen.add(name)
        output = target.joinpath(*path.parts)
        output.parent.mkdir(parents=True, exist_ok=True)
        with source.extractfile(member) as reader:
            output.write_bytes(reader.read())
        output.chmod(0o700 if name.endswith(".sh") or name.endswith("updater-linux-amd64") else 0o600)
if seen != expected:
    raise SystemExit("Updater installer is incomplete")
PYARCHIVE
cmp -s "$work/updater.pem" "$stage/updater/release-trust/updater.pem" || fail "installer carries a different Updater release key"

install -d -o root -g root -m 0755 "$(dirname "$trust_file")"
[ -f "$trust_file" ] || install -o root -g root -m 0644 "$work/updater.pem" "$trust_file"
"$stage/updater/install.sh" --install-host "$stage/updater/updater-linux-amd64"
printf '%s\n' "Updater $version is installed and healthy. Register service heads through their own installers."
