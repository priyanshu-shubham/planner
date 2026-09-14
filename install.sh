#!/bin/sh
# Installs the planner CLI from GitHub releases:
#   curl -fsSL https://raw.githubusercontent.com/priyanshu-shubham/planner/main/install.sh | sh
# PLANNER_VERSION pins a release tag (default: latest); PLANNER_INSTALL_DIR overrides ~/.local/bin.
set -eu

repo=priyanshu-shubham/planner
version=${PLANNER_VERSION:-latest}
dir=${PLANNER_INSTALL_DIR:-$HOME/.local/bin}

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case $os in
  linux | darwin) ;;
  *) echo "planner: unsupported OS '$os'; download a binary from https://github.com/$repo/releases" >&2; exit 1 ;;
esac
case $(uname -m) in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) echo "planner: unsupported architecture '$(uname -m)'" >&2; exit 1 ;;
esac

if [ "$version" = latest ]; then
  url=https://github.com/$repo/releases/latest/download/planner-$os-$arch.gz
else
  url=https://github.com/$repo/releases/download/$version/planner-$os-$arch.gz
fi

mkdir -p "$dir"
# Unpack beside the target and rename over it, so replacing a running planner works.
tmp=$(mktemp "$dir/.planner.XXXXXX")
trap 'rm -f "$tmp"' EXIT
curl -fsSL "$url" | gunzip > "$tmp" || { echo "planner: download failed: $url" >&2; exit 1; }
chmod 755 "$tmp"
mv -f "$tmp" "$dir/planner"

echo "installed planner $("$dir/planner" version) to $dir/planner"
case ":$PATH:" in
  *":$dir:"*) ;;
  *) echo "$dir is not on your PATH; add this to your shell profile: export PATH=\"$dir:\$PATH\"" ;;
esac
