#!/usr/bin/env bash
set -euo pipefail

version="${1:?version is required}"
output="${2:-release-artifacts}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repository="${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"

[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]] || {
  echo "Version must be semantic, for example 1.2.3" >&2
  exit 2
}

mkdir -p "$root/$output"
binary="$root/$output/updater-linux-amd64"
(
  cd "$root"
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -trimpath -ldflags "-s -w -X main.version=${version}" \
    -o "$binary" ./cmd/updater
)

binary_sha="$(sha256sum "$binary" | awk '{print $1}')"
(
  cd "$root/$output"
  printf '%s  %s\n' "$binary_sha" updater-linux-amd64 > updater-linux-amd64.sha256
)

cosign_binary="${COSIGN_BINARY:-$(command -v cosign || true)}"
if [[ -z "$cosign_binary" || ! -x "$cosign_binary" ]]; then
  echo "A cosign binary is required to create release packages" >&2
  exit 3
fi

debroot="$(mktemp -d)"
stage="$(mktemp -d)"
trap 'rm -rf "$debroot" "$stage"' EXIT
mkdir -p "$debroot/DEBIAN" "$debroot/usr/bin" "$debroot/lib/systemd/system"
sed "s/^Version: .*/Version: $version/" \
  "$root/packaging/control" > "$debroot/DEBIAN/control"
cp "$root/packaging/postinst" "$debroot/DEBIAN/postinst"
cp "$binary" "$debroot/usr/bin/updater"
cp "$cosign_binary" "$debroot/usr/bin/cosign"
cp "$root/systemd/updater.service" "$debroot/lib/systemd/system/"
chmod 0755 "$debroot/DEBIAN/postinst" "$debroot/usr/bin/updater" "$debroot/usr/bin/cosign"
dpkg-deb --build --root-owner-group \
  "$debroot" "$root/$output/updater_${version}_amd64.deb"
(
  cd "$root/$output"
  sha256sum "updater_${version}_amd64.deb" > "updater_${version}_amd64.deb.sha256"
)

mkdir -p "$stage/updater/systemd"
cp "$binary" "$stage/updater/updater-linux-amd64"
cp "$root/install.sh" "$stage/updater/install.sh"
cp "$root/systemd/updater.service" "$stage/updater/systemd/"
cp "$cosign_binary" "$stage/updater/cosign-linux-amd64"
chmod 0755 "$stage/updater/"{install.sh,updater-linux-amd64,cosign-linux-amd64}
tar -czf "$root/$output/updater-${version}-install.tar.gz" -C "$stage" updater
(
  cd "$root/$output"
  sha256sum "updater-${version}-install.tar.gz" > "updater-${version}-install.tar.gz.sha256"
)

cat > "$root/$output/updater-release.json" <<EOF
{
  "schema_version": 1,
  "service": "updater",
  "version": "$version",
  "binary": {
    "url": "https://github.com/${repository}/releases/download/updater-v${version}/updater-linux-amd64",
    "sha256": "$binary_sha"
  }
}
EOF
