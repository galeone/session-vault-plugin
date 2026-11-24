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

VERSION="1.8.0"
# SHA-256 of the release binaries of $VERSION, committed here so that what
# runs is checked against this repository, not against a file fetched from
# the same place as the binary. The release workflow (sync-version) rewrites
# this block with every release; the binaries are built by GitHub Actions
# from the tagged source (cli/build-release.sh).
# BEGIN CHECKSUMS
CHECKSUMS="
7914193ac265f55982b88426bd3e3c0c76c7a0d0f7518a49958d65b54902f4cb  session-vault_darwin_amd64
dccc425956a34ab9d43988f2820dbd441e8f925a0bbe958fe1ad2b146e5ae3ef  session-vault_darwin_arm64
b15b5225254d0051855bda22c19811a0896681237959ec1efce71940f04f55e5  session-vault_linux_amd64
e9585a2224ac49ed7e437484c94a49cb29ddf4efe10ecd3f477f081982c81da2  session-vault_linux_arm64
e50ccc726ceb3d2bf3ff8400d639ea5e8ac662e576d9668d9e80703b39e97973  session-vault_windows_amd64.exe
8de41844d9cc5933466f1d64fde8b86765755ab553fd286f29c972d193a2dc8d  session-vault_windows_arm64.exe
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
