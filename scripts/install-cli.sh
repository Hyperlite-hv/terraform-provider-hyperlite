#!/bin/bash
# Installs terraform or tofu of the given version into $3 (a directory on the PATH), checked against the archive's
# SHA-256 recorded here. A SHA256SUMS file fetched from the same server as the archive only proves the download is
# whole, not that the server served the real one: these binaries run in the CI and in the release checks.
# A new version: add its line, with the hash of the published SHA256SUMS (and its signature checked once by hand).
set -euo pipefail
tool="$1" version="$2" dest="$3"

case "$tool $version" in
  "terraform 1.5.7") sha256=c0ed7bc32ee52ae255af9982c8c88a7a4c610485cf1d55feeb037eab75fa082c ;;
  "terraform 1.13.4") sha256=98aa516201e948306698efd9954ab4cc0d1227c2578ba56245898b5f679e590b ;;
  "tofu 1.10.6") sha256=15b7bed76420b50da3e121769c43341df8cd57d751ca14e6dbe9c850124c6dac ;;
  *) echo "no recorded SHA-256 for $tool $version: add it to $0" >&2; exit 2 ;;
esac
case "$tool" in
  terraform) url="https://releases.hashicorp.com/terraform/${version}/terraform_${version}_linux_amd64.zip" ;;
  tofu) url="https://github.com/opentofu/opentofu/releases/download/v${version}/tofu_${version}_linux_amd64.zip" ;;
esac

work="$(mktemp -d)"
curl -fsSL -o "$work/cli.zip" "$url"
echo "$sha256  $work/cli.zip" | sha256sum -c -
mkdir -p "$dest"
unzip -o -q "$work/cli.zip" "$tool" -d "$dest"
"$dest/$tool" version
