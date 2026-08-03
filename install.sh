#!/usr/bin/env sh
set -eu

if [ "$(id -u)" -ne 0 ]; then
  echo "Run this installer as root." >&2
  exit 1
fi

if ! command -v flock >/dev/null 2>&1; then
  echo "flock from util-linux is required." >&2
  exit 1
fi
exec 9>/run/lock/updater-install.lock
flock -x 9

if [ "$#" -lt 2 ] || [ "$#" -gt 3 ]; then
  echo "Usage: updater/install.sh <head-id> <head-env-file> [updater-binary]" >&2
  exit 2
fi

head_id="$1"
head_env="$(readlink -f "$2")"
script_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
binary="${3:-$script_dir/updater-linux-amd64}"

if [ ! -f "$head_env" ]; then
  echo "Head environment file is unavailable: $head_env" >&2
  exit 3
fi
if [ ! -x "$binary" ] && [ ! -f "$binary" ]; then
  echo "Updater binary is unavailable: $binary" >&2
  exit 4
fi

getent group updater >/dev/null 2>&1 || groupadd --system updater
install -d -o root -g updater -m 0750 /etc/exocortex /run/exocortex
install -d -o root -g root -m 0700 /var/lib/updater
candidate_version=$("$binary" version 2>/dev/null || true)
installed_version=""
if [ -x /usr/bin/updater ]; then
  installed_version=$(/usr/bin/updater version 2>/dev/null || true)
fi
if [ -z "$installed_version" ] ||
   [ -z "$candidate_version" ] ||
   dpkg --compare-versions "$candidate_version" gt "$installed_version"; then
  install -m 0755 "$binary" /usr/bin/updater
elif [ "$candidate_version" != "$installed_version" ]; then
  echo "Keeping installed updater $installed_version; bundled $candidate_version is not newer."
fi
if [ -f "$script_dir/cosign-linux-amd64" ]; then
  install -m 0755 "$script_dir/cosign-linux-amd64" /usr/bin/cosign
elif ! command -v cosign >/dev/null 2>&1; then
  echo "cosign is required; use the signed release bundle or install cosign first." >&2
  exit 5
fi
install -m 0644 "$script_dir/systemd/updater.service" /etc/systemd/system/updater.service

/usr/bin/updater register-head "$head_id" "$head_env"
socket_gid="$(getent group updater | cut -d: -f3)"

if grep -q '^UPDATER_SOCKET_GID=' "$head_env"; then
  sed -i "s/^UPDATER_SOCKET_GID=.*/UPDATER_SOCKET_GID=$socket_gid/" "$head_env"
else
  printf '\nUPDATER_SOCKET_GID=%s\n' "$socket_gid" >> "$head_env"
fi

systemctl daemon-reload
systemctl enable --now updater.service
systemctl restart updater.service

echo "updater installed; head '$head_id' is registered"
echo "Unix socket group ID: $socket_gid"
