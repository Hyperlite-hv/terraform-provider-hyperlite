#!/bin/bash
# Installs terraform or tofu of the given version into $2 (a directory on the PATH), checked against the release's
# SHA256SUMS: what the setup actions would do, without depending on them.
set -euo pipefail
tool="$1" version="$2" dest="$3"
case "$tool" in
  terraform)
    base="https://releases.hashicorp.com/terraform/${version}"
    zip="terraform_${version}_linux_amd64.zip"
    sums="terraform_${version}_SHA256SUMS" ;;
  tofu)
    base="https://github.com/opentofu/opentofu/releases/download/v${version}"
    zip="tofu_${version}_linux_amd64.zip"
    sums="tofu_${version}_SHA256SUMS" ;;
  *) echo "unknown tool $tool" >&2; exit 2 ;;
esac
work="$(mktemp -d)"
curl -fsSL -o "$work/$zip" "$base/$zip"
curl -fsSL -o "$work/$sums" "$base/$sums"
(cd "$work" && grep " ${zip}\$" "$sums" | sha256sum -c -)
mkdir -p "$dest"
unzip -o -q "$work/$zip" "$tool" -d "$dest"
"$dest/$tool" version
