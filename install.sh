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
standalone=false
if [ "$#" -eq 2 ] && [ "$1" = --prepare-host ]; then
  head_id=""
  head_env=""
  binary="$2"
  UPDATER_PREPARE_ONLY=true
elif [ "$#" -eq 2 ] && [ "$1" = --install-host ]; then
  head_id=""
  head_env=""
  binary="$2"
  standalone=true
else
  if [ "$#" -lt 2 ] || [ "$#" -gt 3 ]; then
    echo "Usage: updater/install.sh <head-id> <head-env-file> [updater-binary] | --prepare-host <binary> | --install-host <binary>" >&2
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
install -d -o root -g root -m 0755 /etc/exocortex/release-trust
window_public_b64='__WINDOW_PUBLIC_KEY_BASE64__'
window_temp=''
for trust_service in updater neptune gryphon wyvern window; do
  bundled_trust="$script_dir/release-trust/$trust_service.pem"
  # Older non-LLM head bundles may omit this optional trust scope. Wyvern
  # installers require it and VerifyBytes never accepts a missing host pin.
  if [ "$trust_service" = wyvern ] && [ ! -e "$bundled_trust" ]; then continue; fi
  if [ "$trust_service" = window ] && [ ! -e "$bundled_trust" ]; then
    case "$window_public_b64" in
      __WINDOW_*) echo 'The signed Updater installer has no embedded Window trust anchor.' >&2; exit 5 ;;
    esac
    window_temp="$(mktemp /run/window-release-trust.XXXXXX)"
    printf '%s' "$window_public_b64" | base64 -d > "$window_temp"
    openssl pkey -pubin -in "$window_temp" -noout >/dev/null 2>&1 || {
      echo 'The signed Updater installer has invalid Window trust.' >&2
      exit 5
    }
    bundled_trust="$window_temp"
  fi
  [ -f "$bundled_trust" ] && [ ! -L "$bundled_trust" ] || {
    echo "The signed Updater bundle has no release-trust/$trust_service.pem." >&2
    exit 5
  }
  trust_file="/etc/exocortex/release-trust/$trust_service.pem"
  if [ -f "$trust_file" ] && ! cmp -s "$bundled_trust" "$trust_file"; then
    echo "Installed $trust_service release key differs from the signed Updater bundle." >&2
    exit 5
  fi
  [ -f "$trust_file" ] || install -o root -g root -m 0644 "$bundled_trust" "$trust_file"
done
[ -z "$window_temp" ] || rm -f "$window_temp"

updater_env=/etc/exocortex/updater/.env
install -d -o root -g root -m 0700 /etc/exocortex/updater
if [ ! -f "$updater_env" ]; then
  temporary_env="$(mktemp /etc/exocortex/updater/.env.XXXXXX)"
  cat >"$temporary_env" <<'EOF'
UPDATER_SOCKET_PATH=/run/exocortex/updater.sock
UPDATER_STATE_DIR=/var/lib/updater
UPDATER_HEADS_FILE=/etc/exocortex/updater-heads.json
UPDATER_MAX_RETAINED_JOBS=20
UPDATER_RETENTION_DAYS=30
UPDATER_COMMAND_TIMEOUT_SEC=300
NEPTUNE_UPDATER_TOKEN_FILE=/etc/neptune/updater-agent.token
HOME=/var/lib/updater
EXOCORTEX_PREPARED_HOST=true
EOF
  chown root:root "$temporary_env"
  chmod 0600 "$temporary_env"
  mv "$temporary_env" "$updater_env"
fi
chown root:root "$updater_env"
chmod 0600 "$updater_env"
install -d -o root -g root -m 0700 /var/lib/updater
# Provision only fixed helper identities and paths, before the sandbox starts.
# The daemons themselves are installed on demand from verified releases.
getent group neptune >/dev/null 2>&1 || groupadd --system neptune
getent group neptune-clients >/dev/null 2>&1 || groupadd --system neptune-clients
getent group gryphon-clients >/dev/null 2>&1 || groupadd --system gryphon-clients
id neptune >/dev/null 2>&1 || useradd --system --gid neptune --home /var/lib/neptune --shell /usr/sbin/nologin neptune
id gryphon >/dev/null 2>&1 || useradd --system --gid gryphon-clients --home /var/lib/gryphon --shell /usr/sbin/nologin gryphon
getent group window >/dev/null 2>&1 || groupadd --system window
id window >/dev/null 2>&1 || useradd --system --gid window --home /var/lib/window-ssh --shell /bin/sh window
[ "$(getent passwd window | cut -d: -f6-7)" = '/var/lib/window-ssh:/bin/sh' ] || { echo 'Window account identity differs from the dedicated SSH account.' >&2; exit 5; }
[ "$(id -gn window)" = window ] && [ "$(id -nG window)" = window ] || { echo 'Window SSH account has unexpected groups.' >&2; exit 5; }
# The interactive operator is separate from Window's forced-command SSH reader.
# Provision before the Updater daemon starts; its systemd sandbox cannot edit
# local accounts or sudoers. Existing credentials and authorized_keys survive.
if ! command -v visudo >/dev/null 2>&1; then
  command -v apt-get >/dev/null 2>&1 || { echo 'sudo/visudo is required for the Window operator account.' >&2; exit 5; }
  DEBIAN_FRONTEND=noninteractive apt-get install -y sudo
fi
id windowops >/dev/null 2>&1 || useradd --create-home --user-group --shell /bin/bash --password '!' windowops
[ "$(getent passwd windowops | cut -d: -f6-7)" = '/home/windowops:/bin/bash' ] || { echo 'Window operator account must use /home/windowops and /bin/bash.' >&2; exit 5; }
[ "$(id -gn windowops)" = windowops ] || { echo 'Window operator primary group must be windowops.' >&2; exit 5; }
[ "$(id -u windowops)" -ne 0 ] || { echo 'Window operator account must not be root.' >&2; exit 5; }
for privileged_group in root sudo wheel docker lxd updater window; do
  case " $(id -nG windowops) " in *" $privileged_group "*) echo "Remove windowops from privileged group $privileged_group." >&2; exit 5 ;; esac
done
[ ! -L /home/windowops ] || { echo 'Window operator home must not be a symlink.' >&2; exit 5; }
install -d -o windowops -g windowops -m 0700 /home/windowops
install -d -o root -g root -m 0755 /etc/sudoers.d
[ ! -L /etc/sudoers.d/windowops ] || { echo 'Window operator sudoers file must not be a symlink.' >&2; exit 5; }
windowops_sudoers="$(mktemp /etc/sudoers.d/.windowops.XXXXXX)"
printf '%s\n' 'windowops ALL=(root) NOPASSWD: /usr/bin/updater tui --window-only, /usr/bin/updater window pair --key-base64 *, /usr/local/bin/window capture-test *' > "$windowops_sudoers"
chmod 0440 "$windowops_sudoers"
visudo -cf "$windowops_sudoers" >/dev/null || { rm -f "$windowops_sudoers"; exit 5; }
mv -f "$windowops_sudoers" /etc/sudoers.d/windowops
visudo -c >/dev/null
# "NP" is an invalid password hash: password login is impossible while
# OpenSSH can still admit the one forced-command public key with UsePAM=no.
usermod --password NP window
install -d -o root -g root -m 0755 /var/lib/window-ssh /var/lib/window-ssh/.ssh
install -d -o root -g root -m 0700 /var/lib/window /var/lib/window/test-results
install -d -o root -g root -m 0755 /run/window
install -d -o root -g root -m 0700 /run/window-admin
usermod -a -G neptune,neptune-clients,updater neptune
install -d -m 0755 /usr/local/lib/updater /usr/local/lib/neptune /usr/local/lib/gryphon /usr/local/lib/window /usr/local/sbin /usr/local/bin /opt/exocortex
install -d -o root -g updater -m 0750 /etc/exocortex/units
install -d -o root -g neptune -m 0750 /etc/neptune
install -d -o root -g gryphon-clients -m 0750 /etc/gryphon
install -d -o root -g root -m 0755 /etc/wyvern
install -d -o root -g root -m 0755 /etc/systemd/journald@wyvern.conf.d
install -d -o 10001 -g 10001 -m 0750 /run/wyvern /run/wyvern-admin
install -d -o 10001 -g 10001 -m 0700 /var/lib/wyvern
install -d -o root -g root -m 0700 /etc/exocortex/wyvern /etc/exocortex/wyvern/clients
install -d -o neptune -g neptune -m 0700 /var/lib/neptune
install -d -o neptune -g neptune -m 0700 /var/cache/neptune
install -d -o gryphon -g gryphon-clients -m 0700 /var/lib/gryphon
install -d -m 0755 /etc/systemd/system/multi-user.target.wants
for helper in neptune gryphon window; do
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
ln -sfn /usr/local/lib/window/window /usr/local/bin/window
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
if [ "$restart_required" = true ] || [ ! -f /etc/systemd/system/updater.service ] ||
   ! cmp -s "$script_dir/systemd/updater.service" /etc/systemd/system/updater.service; then
  install -m 0644 "$script_dir/systemd/updater.service" /etc/systemd/system/updater.service
  restart_required=true
fi

socket_gid="$(getent group updater | cut -d: -f3)"

if [ "$standalone" != true ]; then
  /usr/bin/updater register-head "$head_id" "$head_env"
  if grep -q '^UPDATER_SERVICE_ID=saturn$' "$head_env" && ! grep -q '^UPDATER_HOST_RECOVERY_ALLOWED=' "$head_env"; then
    printf '\nUPDATER_HOST_RECOVERY_ALLOWED=true\n' >> "$head_env"
  fi
  if grep -q '^UPDATER_SOCKET_GID=' "$head_env"; then
    sed -i "s/^UPDATER_SOCKET_GID=.*/UPDATER_SOCKET_GID=$socket_gid/" "$head_env"
  else
    printf '\nUPDATER_SOCKET_GID=%s\n' "$socket_gid" >> "$head_env"
  fi
fi

# The installer knows only its own repository. Keep it as the host-owned
# fallback until the operator configures Updater's machine connection.
/usr/bin/updater host seed-source updater https://github.com/psewdon1m-exocortex/updater

if [ "$restart_required" = true ]; then
  systemctl daemon-reload
  systemctl enable updater.service
  systemctl restart updater.service
elif ! systemctl is-active --quiet updater.service; then
  systemctl enable --now updater.service
fi

if [ "$standalone" = true ]; then
  echo "updater installed; no service head was registered"
else
  echo "updater installed; head '$head_id' is registered"
fi
echo "Unix socket group ID: $socket_gid"
