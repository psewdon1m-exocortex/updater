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

script_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
if [ "$#" -eq 2 ] && [ "$1" = --prepare-host ]; then
  head_id=""
  head_env=""
  binary="$2"
  UPDATER_PREPARE_ONLY=true
else
  if [ "$#" -lt 2 ] || [ "$#" -gt 3 ]; then
    echo "Usage: updater/install.sh <head-id> <head-env-file> [updater-binary] | --prepare-host <binary>" >&2
    exit 2
  fi
  head_id="$1"
  head_env="$(readlink -f "$2")"
  binary="${3:-$script_dir/updater-linux-amd64}"
  [ -f "$head_env" ] || { echo "Head environment file is unavailable." >&2; exit 3; }
fi
[ -f "$binary" ] || { echo "Updater binary is unavailable." >&2; exit 4; }

getent group updater >/dev/null 2>&1 || groupadd --system updater
install -d -o root -g updater -m 0750 /etc/exocortex /run/exocortex
bundled_trust="$script_dir/release-trust/updater.pem"
if [ -f "$bundled_trust" ]; then
  trust_file=/etc/exocortex/release-trust/updater.pem
  install -d -o root -g root -m 0755 /etc/exocortex/release-trust
  if [ -f "$trust_file" ] && ! cmp -s "$bundled_trust" "$trust_file"; then
    echo "Installed Updater release key differs from the signed bundle." >&2
    exit 5
  fi
  [ -f "$trust_file" ] || install -o root -g root -m 0644 "$bundled_trust" "$trust_file"
fi
install -d -o root -g root -m 0700 /var/lib/updater
# Provision only fixed helper identities and paths, before the sandbox starts.
# The daemons themselves are installed on demand from verified releases.
getent group neptune >/dev/null 2>&1 || groupadd --system neptune
getent group neptune-clients >/dev/null 2>&1 || groupadd --system neptune-clients
getent group gryphon-clients >/dev/null 2>&1 || groupadd --system gryphon-clients
id neptune >/dev/null 2>&1 || useradd --system --gid neptune --home /var/lib/neptune --shell /usr/sbin/nologin neptune
id gryphon >/dev/null 2>&1 || useradd --system --gid gryphon-clients --home /var/lib/gryphon --shell /usr/sbin/nologin gryphon
usermod -a -G neptune,neptune-clients,updater neptune
install -d -m 0755 /usr/local/lib/updater /usr/local/lib/neptune /usr/local/lib/gryphon /usr/local/sbin /opt/exocortex
install -d -o root -g updater -m 0750 /etc/exocortex/units
install -d -o root -g neptune -m 0750 /etc/neptune
install -d -o root -g gryphon-clients -m 0750 /etc/gryphon
install -d -o neptune -g neptune -m 0700 /var/lib/neptune
install -d -o neptune -g neptune -m 0700 /var/cache/neptune
install -d -o gryphon -g gryphon-clients -m 0700 /var/lib/gryphon
install -d -m 0755 /etc/systemd/system/multi-user.target.wants
for helper in neptune gryphon; do
  unit="/etc/systemd/system/$helper.service"
  if [ -f "$unit" ] && [ ! -L "$unit" ]; then
    install -m 0644 "$unit" "/etc/exocortex/units/$helper.service"
  fi
  ln -sfn "/etc/exocortex/units/$helper.service" "$unit"
  ln -sfn "$unit" "/etc/systemd/system/multi-user.target.wants/$helper.service"
done
if [ -f /usr/local/sbin/neptunectl ] && [ ! -L /usr/local/sbin/neptunectl ]; then
  install -m 0755 /usr/local/sbin/neptunectl /usr/local/lib/neptune/neptunectl
fi
if [ -f /usr/local/sbin/gryphon ] && [ ! -L /usr/local/sbin/gryphon ]; then
  install -m 0755 /usr/local/sbin/gryphon /usr/local/lib/gryphon/gryphonctl
fi
ln -sfn /usr/local/lib/neptune/neptunectl /usr/local/sbin/neptunectl
ln -sfn /usr/local/lib/gryphon/gryphonctl /usr/local/sbin/gryphon
# A dedicated writable directory makes atomic self replacement possible.
if [ -f /usr/bin/updater ] && [ ! -L /usr/bin/updater ]; then
  install -m 0755 /usr/bin/updater /usr/local/lib/updater/updater
fi
ln -sfn /usr/local/lib/updater/updater /usr/bin/updater
candidate_version=$("$binary" version 2>/dev/null || true)
restart_required=false
[ -n "$candidate_version" ] || { echo "Bundled updater has no valid version." >&2; exit 4; }
# This typed mode is invoked by the verified self-update supervisor. It migrates
# host permissions and the unit without replacing/restarting the running daemon.
if [ "${UPDATER_PREPARE_ONLY:-false}" = true ]; then
  install -m 0644 "$script_dir/systemd/updater.service" /etc/systemd/system/updater.service
  if [ -n "$head_env" ] && grep -q '^UPDATER_SERVICE_ID=saturn$' "$head_env" && ! grep -q '^UPDATER_HOST_RECOVERY_ALLOWED=' "$head_env"; then
    printf '\nUPDATER_HOST_RECOVERY_ALLOWED=true\n' >> "$head_env"
  fi
  systemctl daemon-reload
  exit 0
fi
installed_version=""
if [ -x /usr/bin/updater ]; then
  installed_version=$(/usr/bin/updater version 2>/dev/null || true)
fi
if [ -z "$installed_version" ] ||
   dpkg --compare-versions "$candidate_version" gt "$installed_version"; then
  install -m 0755 "$binary" /usr/local/lib/updater/updater
  restart_required=true
elif [ "$candidate_version" != "$installed_version" ]; then
  echo "Keeping installed updater $installed_version; bundled $candidate_version is not newer."
fi
if [ "$restart_required" = true ] || [ ! -f /etc/systemd/system/updater.service ]; then
  install -m 0644 "$script_dir/systemd/updater.service" /etc/systemd/system/updater.service
  restart_required=true
fi

/usr/bin/updater register-head "$head_id" "$head_env"
if grep -q '^UPDATER_SERVICE_ID=saturn$' "$head_env" && ! grep -q '^UPDATER_HOST_RECOVERY_ALLOWED=' "$head_env"; then
  printf '\nUPDATER_HOST_RECOVERY_ALLOWED=true\n' >> "$head_env"
fi
socket_gid="$(getent group updater | cut -d: -f3)"

if grep -q '^UPDATER_SOCKET_GID=' "$head_env"; then
  sed -i "s/^UPDATER_SOCKET_GID=.*/UPDATER_SOCKET_GID=$socket_gid/" "$head_env"
else
  printf '\nUPDATER_SOCKET_GID=%s\n' "$socket_gid" >> "$head_env"
fi

if [ "$restart_required" = true ]; then
  systemctl daemon-reload
  systemctl enable updater.service
  systemctl restart updater.service
elif ! systemctl is-active --quiet updater.service; then
  systemctl enable --now updater.service
fi

echo "updater installed; head '$head_id' is registered"
echo "Unix socket group ID: $socket_gid"
