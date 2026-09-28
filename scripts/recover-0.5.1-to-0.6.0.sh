#!/usr/bin/env bash
set -euo pipefail
umask 077

# One-time bridge for Updater 0.5.1: its self-update rejects the additional
# wyvern.pem member in the signed 0.6.0 installer. Run as root on the host.
[[ $(id -u) -eq 0 ]] || { echo 'Run as root.' >&2; exit 1; }
[[ $(/usr/bin/updater version) == 0.5.1 ]] || {
  echo 'This recovery is only for an installed Updater 0.5.1.' >&2
  exit 1
}
trust=/etc/exocortex/release-trust/updater.pem
[[ -f $trust && ! -L $trust ]] || { echo 'Pinned Updater release key is missing.' >&2; exit 1; }
for command in curl openssl python3 systemctl; do
  command -v "$command" >/dev/null || { echo "$command is required." >&2; exit 1; }
done
/usr/bin/updater jobs | python3 -c 'import json, sys; jobs=json.load(sys.stdin); active=[job["id"] for job in jobs if job["state"] not in ("COMPLETED", "FAILED", "ROLLED_BACK", "ROLLBACK_FAILED")]; sys.exit("An Updater operation is still active: " + ", ".join(active) if active else 0)'

stage=$(mktemp -d)
changed=false
cleanup() {
  local code=$?
  trap - EXIT
  if [[ $changed == true && $code -ne 0 ]]; then
    echo 'Restoring Updater 0.5.1 binary and service unit.' >&2
    local rollback_failed=false
    systemctl stop updater.service || rollback_failed=true
    install -m 0755 "$stage/previous-updater" /usr/local/lib/updater/updater || rollback_failed=true
    install -m 0644 "$stage/previous-unit" /etc/systemd/system/updater.service || rollback_failed=true
    systemctl daemon-reload || rollback_failed=true
    systemctl restart updater.service || rollback_failed=true
    if [[ $rollback_failed == true ]]; then
      echo "Rollback needs operator repair; backups remain at $stage" >&2
      exit "$code"
    fi
  fi
  rm -rf -- "$stage"
  exit "$code"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

base=https://github.com/psewdon1m-exocortex/updater/releases/download/updater-v0.6.0
curl -fsSL --proto '=https' --proto-redir '=https' --retry 3 --connect-timeout 10 \
  --max-time 120 --max-filesize 2097152 "$base/updater-release.json" -o "$stage/manifest.json"
curl -fsSL --proto '=https' --proto-redir '=https' --retry 3 --connect-timeout 10 \
  --max-time 120 --max-filesize 16384 "$base/updater-release.json.sig.json" -o "$stage/signature.json"
curl -fsSL --proto '=https' --proto-redir '=https' --retry 3 --connect-timeout 10 \
  --max-time 300 --max-filesize 83886080 "$base/updater-0.6.0-install.tar.gz" -o "$stage/installer.tar.gz"

python3 - "$stage" "$trust" "$base" <<'PY'
import base64, hashlib, json, os, pathlib, re, subprocess, sys, tarfile

stage, trust = map(pathlib.Path, sys.argv[1:3])
base = sys.argv[3]
manifest_path = stage / 'manifest.json'
manifest = json.loads(manifest_path.read_bytes())
signature = json.loads((stage / 'signature.json').read_bytes())
if (manifest.get('schema_version'), manifest.get('service'), manifest.get('version')) != (1, 'updater', '0.6.0'):
    raise SystemExit('Signed manifest has the wrong release identity')
if signature.get('schema') != 'exocortex.release-signature.v1' or signature.get('algorithm') != 'RSA-PSS-SHA256':
    raise SystemExit('Invalid release signature envelope')
der = subprocess.run(['openssl', 'pkey', '-pubin', '-in', str(trust), '-outform', 'DER'], check=True, capture_output=True).stdout
if hashlib.sha256(der).hexdigest() != signature.get('key_id'):
    raise SystemExit('Release signer differs from the installed pin')
sig_path = stage / 'signature.bin'
sig_path.write_bytes(base64.b64decode(signature.get('signature', ''), validate=True))
subprocess.run(['openssl', 'dgst', '-sha256', '-verify', str(trust), '-signature', str(sig_path),
                '-sigopt', 'rsa_padding_mode:pss', '-sigopt', 'rsa_pss_saltlen:32', str(manifest_path)],
               check=True, stdout=subprocess.DEVNULL)

binary = manifest.get('binary') or {}
installer = manifest.get('installer') or {}
if binary.get('url') != base + '/updater-linux-amd64':
    raise SystemExit('Binary URL differs from the selected release')
if installer.get('url') != base + '/updater-0.6.0-install.tar.gz':
    raise SystemExit('Installer URL differs from the selected release')
for asset in (binary, installer):
    if not re.fullmatch(r'[0-9a-f]{64}', str(asset.get('sha256', ''))):
        raise SystemExit('Invalid signed SHA-256 value')
archive = stage / 'installer.tar.gz'
if hashlib.sha256(archive.read_bytes()).hexdigest() != installer['sha256']:
    raise SystemExit('Installer digest differs from the signed manifest')

expected = {
    'updater/updater-linux-amd64': 64 * 1024 * 1024,
    'updater/install.sh': 256 * 1024,
    'updater/systemd/updater.service': 64 * 1024,
    'updater/release-trust/updater.pem': 16 * 1024,
    'updater/release-trust/neptune.pem': 16 * 1024,
    'updater/release-trust/gryphon.pem': 16 * 1024,
    'updater/release-trust/wyvern.pem': 16 * 1024,
}
target = stage / 'verified'
seen = set()
with tarfile.open(archive, 'r:gz') as members:
    for member in members:
        if member.isdir() and member.name in ('updater', 'updater/systemd', 'updater/release-trust'):
            continue
        if member.name not in expected or member.name in seen or not member.isfile() or not 0 < member.size <= expected.get(member.name, 0):
            raise SystemExit('Unexpected installer archive member')
        seen.add(member.name)
        path = target.joinpath(*pathlib.PurePosixPath(member.name).parts)
        path.parent.mkdir(parents=True, exist_ok=True)
        with members.extractfile(member) as source, path.open('xb') as destination:
            destination.write(source.read())
if seen != set(expected):
    raise SystemExit('Installer archive is incomplete')
if hashlib.sha256((target / 'updater/updater-linux-amd64').read_bytes()).hexdigest() != binary['sha256']:
    raise SystemExit('Bundled binary differs from the signed manifest')
if (target / 'updater/release-trust/updater.pem').read_bytes() != trust.read_bytes():
    raise SystemExit('Installer release key differs from the installed pin')
os.chmod(target / 'updater/updater-linux-amd64', 0o755)
PY

[[ $("$stage/verified/updater/updater-linux-amd64" version) == 0.6.0 ]] || {
  echo 'Verified release binary reports the wrong version.' >&2
  exit 1
}
install -m 0755 /usr/local/lib/updater/updater "$stage/previous-updater"
install -m 0644 /etc/systemd/system/updater.service "$stage/previous-unit"
changed=true
systemctl stop updater.service
sh "$stage/verified/updater/install.sh" --install-host "$stage/verified/updater/updater-linux-amd64"
[[ $(/usr/bin/updater version) == 0.6.0 ]] || { echo 'Installed binary has the wrong version.' >&2; exit 1; }
ready=false
for attempt in {1..60}; do
  if /usr/bin/updater status 2>/dev/null | python3 -c 'import json,sys; status=json.load(sys.stdin); sys.exit(0 if status.get("version") == "0.6.0" else 1)' 2>/dev/null; then
    ready=true
    break
  fi
  sleep 0.5
done
if [[ $ready != true ]]; then
  echo 'Updater 0.6.0 did not become healthy within 30 seconds.' >&2
  systemctl status updater.service --no-pager -l >&2 || true
  journalctl -u updater.service -n 30 --no-pager >&2 || true
  exit 1
fi
changed=false
echo 'Updater 0.6.0 is installed and healthy.'
