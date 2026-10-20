#!/bin/sh
# Install a verified release and prepare local defaults. No Go, jq, Node or gh.
set -eu
umask 077

fail() { printf 'Stalemate: %s\n' "$*" >&2; exit 1; }
say() { printf '%s\n' "$*"; }
version=${STALEMATE_VERSION:-latest}
install_dir=${STALEMATE_INSTALL_DIR:-"${HOME:?HOME must be set}/.local/bin"}
no_start=${STALEMATE_NO_START:-0}
no_path=${STALEMATE_NO_PATH:-0}
no_deps=${STALEMATE_NO_DEPS:-0}
openai=0
listen=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --no-start) no_start=1 ;;
    --no-path) no_path=1 ;;
    --no-deps) no_deps=1 ;;
    --openai) openai=1 ;;
    --version|--install-dir|--listen)
      [ "$#" -ge 2 ] || fail "$1 requires a value"
      case "$1" in --version) version=$2;; --install-dir) install_dir=$2;; --listen) listen=$2;; esac
      shift ;;
    --help) say 'Usage: install.sh [--version vX.Y.Z] [--install-dir DIR] [--no-start] [--no-path] [--no-deps] [--openai] [--listen HOST:PORT]'; exit 0 ;;
    *) fail "Unknown option: $1" ;;
  esac
  shift
done

case $(uname -s) in Darwin) os=darwin;; Linux) os=linux;; *) fail 'This installer supports macOS and Linux; Windows binaries are available in Releases.';; esac
case $(uname -m) in x86_64|amd64) arch=amd64;; arm64|aarch64) arch=arm64;; *) fail 'Supported architectures: x86_64 and arm64.';; esac
command -v curl >/dev/null 2>&1 || fail 'curl is required to download releases.'
command -v tar >/dev/null 2>&1 || fail 'tar is required to extract releases.'
if command -v sha256sum >/dev/null 2>&1; then checksum=sha256sum
elif command -v shasum >/dev/null 2>&1; then checksum=shasum
else fail 'Install sha256sum (coreutils) or shasum before continuing.'; fi

if ! command -v git >/dev/null 2>&1 || ! git --version >/dev/null 2>&1; then
  [ "$no_deps" != 1 ] || fail 'Git is required. Install Git or omit --no-deps.'
  say 'Git is missing; installing it with the system package manager.'
  as_root() {
    if [ "$(id -u)" = 0 ]; then "$@"
    elif command -v sudo >/dev/null 2>&1; then sudo "$@"
    else fail 'Installing Git needs administrator access. Install Git and rerun this command.'; fi
  }
  if command -v brew >/dev/null 2>&1; then brew install git
  elif command -v apt-get >/dev/null 2>&1; then as_root apt-get update; as_root apt-get install -y git ca-certificates
  elif command -v dnf >/dev/null 2>&1; then as_root dnf install -y git ca-certificates
  elif command -v yum >/dev/null 2>&1; then as_root yum install -y git ca-certificates
  elif command -v apk >/dev/null 2>&1; then as_root apk add --no-cache git ca-certificates
  elif command -v pacman >/dev/null 2>&1; then as_root pacman -S --needed --noconfirm git ca-certificates
  elif command -v zypper >/dev/null 2>&1; then as_root zypper --non-interactive install git ca-certificates
  elif [ "$os" = darwin ]; then fail 'Install Apple command line tools with xcode-select --install, then rerun. Alternatively install Git with Homebrew.'
  else fail 'No supported package manager found. Install Git and rerun.'; fi
  git --version >/dev/null 2>&1 || fail 'Git is still unavailable after package installation.'
fi

if [ "$version" = latest ]; then base=https://github.com/jakecyr/stalemate/releases/latest/download
else
  printf '%s\n' "$version" | grep -Eq '^v?[0-9]+\.[0-9]+\.[0-9]+$' || fail 'Version must be latest or vX.Y.Z.'
  case "$version" in v*) ;; *) version=v$version;; esac
  base=https://github.com/jakecyr/stalemate/releases/download/$version
fi
# Explicit mirror override also permits a loopback HTTP server for installer tests.
base=${STALEMATE_DOWNLOAD_BASE_URL:-$base}
case "$base" in https://*) ;; http://127.0.0.1:*|http://localhost:*) ;; *) fail 'Release downloads require HTTPS (except a loopback test mirror).';; esac
archive=stalemate_${os}_${arch}.tar.gz
tmp=$(mktemp -d "${TMPDIR:-/tmp}/stalemate-install.XXXXXX")
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
download() { curl --fail --silent --show-error --location --retry 3 --connect-timeout 15 --max-time 180 --proto '=https,http' --proto-redir '=https' "$1" -o "$2"; }
say "Downloading Stalemate $version for $os/$arch..."
download "$base/$archive" "$tmp/$archive" || fail 'Release download failed. Check the release/version and try again; no existing binary was changed.'
download "$base/checksums.txt" "$tmp/checksums.txt" || fail 'Could not download release checksums.'
expected=$(awk -v name="$archive" '$2 == name { print $1 }' "$tmp/checksums.txt")
printf '%s\n' "$expected" | grep -Eq '^[a-fA-F0-9]{64}$' || fail 'Missing or malformed archive checksum.'
if [ "$checksum" = shasum ]; then actual=$(shasum -a 256 "$tmp/$archive" | awk '{print $1}')
else actual=$(sha256sum "$tmp/$archive" | awk '{print $1}'); fi
[ "$actual" = "$expected" ] || fail 'Checksum mismatch. Nothing was installed; download the release again.'
tar -xzf "$tmp/$archive" -C "$tmp" stalemate
[ -f "$tmp/stalemate" ] && [ ! -L "$tmp/stalemate" ] || fail 'Archive does not contain a regular stalemate executable.'

case "$install_dir" in /*) ;; *) install_dir=$PWD/$install_dir;; esac
case "$install_dir" in *'
'*) fail 'Install directory cannot contain a newline.';; esac
mkdir -p "$install_dir"
cp "$tmp/stalemate" "$install_dir/.stalemate-new-$$"
chmod 755 "$install_dir/.stalemate-new-$$"
mv -f "$install_dir/.stalemate-new-$$" "$install_dir/stalemate"
say "Installed: $install_dir/stalemate"
"$install_dir/stalemate" version

if [ "$no_path" != 1 ]; then
  case ":$PATH:" in *":$install_dir:"*) ;;
    *)
      case ${SHELL:-} in */zsh) profile=$HOME/.zshrc;; */bash) if [ "$os" = darwin ]; then profile=$HOME/.bash_profile; else profile=$HOME/.bashrc; fi;; *) profile=$HOME/.profile;; esac
      escaped=$(printf '%s' "$install_dir" | sed "s/'/'\\\\''/g")
      line="export PATH='$escaped':\"\$PATH\" # stalemate installer"
      if [ ! -f "$profile" ] || ! grep -Fqx "$line" "$profile"; then
        printf '\n%s\n' "$line" >> "$profile" || fail "Could not add PATH to $profile; use $install_dir/stalemate directly."
      fi
      say "PATH added to $profile. Open a new terminal to use the stalemate command." ;;
  esac
fi
set -- setup
[ "$no_start" != 1 ] || set -- "$@" --no-start
[ "$openai" != 1 ] || set -- "$@" --openai
[ -z "$listen" ] || set -- "$@" --listen "$listen"
"$install_dir/stalemate" "$@"
