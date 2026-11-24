#!/usr/bin/env bash
# Locates the session-vault client binary and runs it, downloading a prebuilt
# release on first use. Needs only bash and curl, both of which Claude Code
# already requires (Git Bash on Windows).
#
# Resolution order:
#   1. $SESSION_VAULT_BIN            explicit override
#   2. session-vault on $PATH        e.g. installed with `go install` / built from source
#   3. cached download               ~/.local/share/session-vault/bin/
set -euo pipefail

VERSION="1.7.0"
# SHA-256 of the release binaries of $VERSION, committed here so that what
# runs is checked against this repository, not against a file fetched from
# the same place as the binary. The release workflow (sync-version) rewrites
# this block with every release; the binaries are built by GitHub Actions
# from the tagged source (cli/build-release.sh).
# BEGIN CHECKSUMS
CHECKSUMS="
9739b5849b8a7e51ed80ea426a65748ab6e5b9291ca6a315f6f4081a8c1245e5  session-vault_darwin_amd64
59579eadf98605d2dba9e73e5624a7f9e43fddbc40f7a3917f71e8859181f4a2  session-vault_darwin_arm64
c5e51308fdc2a560e6ef6f4deec767ef58473530fbfc333f98532d2de8f76892  session-vault_linux_amd64
3712c12637bc614e3f04a7aa51f325fd42244dec753ac134d51a610806e8849b  session-vault_linux_arm64
bded57d604051b60fc7dfe75267c2e117e8c50d4d6d02e7859aff0838b28621a  session-vault_windows_amd64.exe
0503e9ddd81fcde45055c3585d66b2701436a8d12a705fb2c2eb87377e5a5807  session-vault_windows_arm64.exe
"
# END CHECKSUMS
RELEASE_BASE="${SESSION_VAULT_RELEASE_BASE:-https://github.com/galeone/session-vault-plugin/releases/download/v$VERSION}"

if [[ -n "${SESSION_VAULT_BIN:-}" ]]; then
  exec "$SESSION_VAULT_BIN" "$@"
fi

if command -v session-vault >/dev/null 2>&1; then
  exec session-vault "$@"
fi

case "$(uname -s)" in
Linux) os=linux ;;
Darwin) os=darwin ;;
MINGW* | MSYS* | CYGWIN* | Windows_NT) os=windows ;;
*)
  echo "session-vault: unsupported OS: $(uname -s)" >&2
  exit 1
  ;;
esac
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*)
  echo "session-vault: unsupported architecture: $(uname -m)" >&2
  exit 1
  ;;
esac

ext=""
[[ "$os" == windows ]] && ext=".exe"

cache_dir="${XDG_DATA_HOME:-$HOME/.local/share}/session-vault/bin"
bin="$cache_dir/session-vault-$VERSION$ext"

artifact="session-vault_${os}_${arch}${ext}"
if [[ ! -x "$bin" ]]; then
  mkdir -p "$cache_dir"
  url="$RELEASE_BASE/$artifact"
  echo "session-vault: downloading $url" >&2
  curl -fsSL "$url" -o "$bin.tmp" || {
    echo "session-vault: download failed: $url" >&2
    echo "session-vault: alternatively, build from source: go build -o session-vault ./cli (in the plugin repo) and put it on PATH" >&2
    exit 1
  }
  # Verify against the checksums pinned above before making it executable,
  # so a corrupted or tampered download is never run.
  expected=$(awk -v f="$artifact" '$2 == f {print $1}' <<<"$CHECKSUMS")
  if [[ -z "$expected" ]]; then
    echo "session-vault: no pinned checksum for $artifact; refusing to install an unverified binary" >&2
    rm -f "$bin.tmp"
    exit 1
  fi
  # sha256sum is GNU coreutils (Linux, Git Bash); stock macOS ships
  # shasum instead.
  if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$bin.tmp" | awk '{print $1}')
  elif command -v shasum >/dev/null 2>&1; then
    actual=$(shasum -a 256 "$bin.tmp" | awk '{print $1}')
  else
    echo "session-vault: need sha256sum or shasum to verify the download" >&2
    exit 1
  fi
  if [[ "$actual" != "$expected" ]]; then
    echo "session-vault: checksum mismatch for $artifact (expected $expected, got $actual)" >&2
    rm -f "$bin.tmp"
    exit 1
  fi
  chmod +x "$bin.tmp"
  mv "$bin.tmp" "$bin"
fi

exec "$bin" "$@"
