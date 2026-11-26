#!/bin/sh
# Download one released Stageproof archive and verify it before the binary is used.
set -eu

version="${STAGEPROOF_VERSION:-v0.5.3}"
if ! printf '%s\n' "$version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
  echo "stageproof: version must look like v0.5.3" >&2
  exit 2
fi

if [ -z "${HELM_PLUGIN_DIR:-}" ]; then
  echo "stageproof: HELM_PLUGIN_DIR is empty" >&2
  exit 2
fi

case "$(uname -m)" in
  x86_64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *)
    echo "stageproof: unsupported architecture $(uname -m)" >&2
    exit 2
    ;;
esac

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  linux | darwin) ;;
  *)
    echo "stageproof: unsupported operating system ${os}" >&2
    exit 2
    ;;
esac

plain="${version#v}"
archive="stageproof_${plain}_${os}_${arch}.tar.gz"
url="https://github.com/stageproof/stageproof/releases/download/${version}"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

curl -fsSL -o "${work}/checksums.txt" "${url}/checksums.txt"
curl -fsSL -o "${work}/${archive}" "${url}/${archive}"

(
  cd "$work"
  # Match the archive name exactly. The SBOM line ends in .tar.gz.sbom.json.
  if ! awk -v name="$archive" '$2 == name { print; found=1 } END { exit !found }' checksums.txt > check-one.txt; then
    echo "stageproof: checksums.txt has no line for ${archive}" >&2
    exit 2
  fi
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum -c check-one.txt
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 -c check-one.txt
  else
    echo "stageproof: need sha256sum or shasum" >&2
    exit 2
  fi
)

mkdir -p "${HELM_PLUGIN_DIR}/bin"
tar -xzf "${work}/${archive}" -C "$work" stageproof
mv "${work}/stageproof" "${HELM_PLUGIN_DIR}/bin/stageproof"
chmod 755 "${HELM_PLUGIN_DIR}/bin/stageproof"
