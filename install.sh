#!/bin/sh
# Install a verified release binary without root or a compiler.
set -eu

version=${SPARESTEP_VERSION:-v0.2.0}
install_dir=${SPARESTEP_INSTALL_DIR:-"$HOME/.local/bin"}
case "$(uname -s)" in
  Linux) os=linux ;;
  *) echo 'This preview installer supports Linux. See the repository for other builds.' >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo 'Supported Linux architectures: x86_64 and arm64.' >&2; exit 1 ;;
esac

asset="sparestep-$os-$arch"
base="https://github.com/ArchieOS-org/sparestep/releases/download/$version"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
if [ -n "${SPARESTEP_RELEASE_DIR:-}" ]; then
  cp "$SPARESTEP_RELEASE_DIR/$asset" "$tmp/$asset"
  cp "$SPARESTEP_RELEASE_DIR/SHA256SUMS" "$tmp/SHA256SUMS"
else
  command -v curl >/dev/null 2>&1 || { echo 'Install curl, then run this installer again.' >&2; exit 1; }
  curl -fL --retry 2 "$base/$asset" -o "$tmp/$asset"
  curl -fL --retry 2 "$base/SHA256SUMS" -o "$tmp/SHA256SUMS"
fi
command -v sha256sum >/dev/null 2>&1 || { echo 'sha256sum is required to verify the download.' >&2; exit 1; }
expected=$(awk -v name="$asset" '$2==name {print $1}' "$tmp/SHA256SUMS")
[ -n "$expected" ] || { echo 'No checksum found for this binary.' >&2; exit 1; }
actual=$(sha256sum "$tmp/$asset" | awk '{print $1}')
[ "$actual" = "$expected" ] || { echo 'Download checksum did not match. Nothing installed.' >&2; exit 1; }
mkdir -p "$install_dir"
install -m 0755 "$tmp/$asset" "$install_dir/sparestep.new"
mv "$install_dir/sparestep.new" "$install_dir/sparestep"
"$install_dir/sparestep" version
"$install_dir/sparestep" install-skill
printf '\nInstalled: %s/sparestep\n' "$install_dir"
printf 'In a Codex task for your project, type / and choose Sparestep.\n'
