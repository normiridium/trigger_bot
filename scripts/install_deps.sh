#!/usr/bin/env bash
set -euo pipefail

# Dependency installer for trigger_admin_bot.
# Defaults are conservative and safe for Debian/Ubuntu VPSes:
# - installs OS packages from apt;
# - installs/upgrades yt-dlp from its official GitHub release asset;
# - leaves MongoDB and vot-cli optional because they alter services/global npm state.
#
# Optional flags:
#   INSTALL_MONGODB=1          install MongoDB Community Edition from MongoDB apt repo
#   MONGODB_VERSION=8.0        override MongoDB repo version (auto-picks 6.0 on Debian bullseye)
#   INSTALL_NODESOURCE=1       install Node.js from NodeSource before installing nodejs
#   NODE_MAJOR=22              NodeSource major version when INSTALL_NODESOURCE=1
#   INSTALL_YTDLP=0            skip yt-dlp GitHub release install
#   INSTALL_VOT_CLI=1          npm install -g vot-cli (requires Node.js 18+)

log() {
  printf '\n==> %s\n' "$*"
}

warn() {
  printf 'WARN: %s\n' "$*" >&2
}

err() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

if [[ "${EUID}" -eq 0 ]]; then
  SUDO=()
else
  command -v sudo >/dev/null 2>&1 || err "sudo is required when not running as root"
  SUDO=(sudo)
fi

command -v apt-get >/dev/null 2>&1 || err "This installer supports apt-get only (Debian/Ubuntu)."

apt_install() {
  DEBIAN_FRONTEND=noninteractive "${SUDO[@]}" apt-get install -y --no-install-recommends "$@"
}

node_major_version() {
  if ! command -v node >/dev/null 2>&1; then
    return 1
  fi
  node -p 'Number(process.versions.node.split(".")[0])' 2>/dev/null || return 1
}

install_nodesource_repo() {
  local major="${NODE_MAJOR:-22}"
  [[ "$major" =~ ^[0-9]+$ ]] || err "NODE_MAJOR must be a number, got: $major"
  log "Adding NodeSource Node.js ${major}.x repository"
  curl -fsSL "https://deb.nodesource.com/setup_${major}.x" | "${SUDO[@]}" -E bash -
}

install_ytdlp_release() {
  if [[ "${INSTALL_YTDLP:-1}" != "1" ]]; then
    warn "Skipping yt-dlp release install because INSTALL_YTDLP=${INSTALL_YTDLP:-0}"
    return 0
  fi

  log "Installing latest yt-dlp release to /usr/local/bin/yt-dlp"
  local tmp
  tmp="$(mktemp)"
  curl -fL --retry 3 --retry-delay 2 \
    https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp \
    -o "$tmp"
  chmod 0755 "$tmp"
  "${SUDO[@]}" install -m 0755 "$tmp" /usr/local/bin/yt-dlp
  rm -f "$tmp"
}

install_vot_cli() {
  if [[ "${INSTALL_VOT_CLI:-0}" != "1" ]]; then
    return 0
  fi
  command -v npm >/dev/null 2>&1 || err "npm is required for INSTALL_VOT_CLI=1"
  local major
  major="$(node_major_version || true)"
  if [[ -z "$major" || "$major" -lt 18 ]]; then
    err "vot-cli requires Node.js 18+. Current node major: ${major:-not installed}. Use INSTALL_NODESOURCE=1 NODE_MAJOR=22."
  fi
  log "Installing vot-cli globally via npm"
  "${SUDO[@]}" npm install -g vot-cli
}

mongo_repo_defaults() {
  # Outputs: "version repo_codename component"
  local id="$1" codename="$2" version="${MONGODB_VERSION:-}" component
  component="main"

  case "$id:$codename" in
    debian:bookworm)
      [[ -n "$version" ]] || version="8.0"
      component="main"
      ;;
    debian:bullseye)
      # MongoDB 8.0 official Debian instructions target bookworm. Bullseye VPSes
      # should stay on the older known-good repo unless explicitly overridden.
      [[ -n "$version" ]] || version="6.0"
      component="main"
      ;;
    ubuntu:noble|ubuntu:jammy)
      [[ -n "$version" ]] || version="8.0"
      component="multiverse"
      ;;
    *)
      return 1
      ;;
  esac

  printf '%s %s %s\n' "$version" "$codename" "$component"
}

install_mongodb() {
  if [[ "${INSTALL_MONGODB:-0}" != "1" ]]; then
    return 0
  fi

  [[ -f /etc/os-release ]] || err "/etc/os-release not found; cannot auto-install MongoDB."
  # shellcheck disable=SC1091
  . /etc/os-release
  local id="${ID:-}" codename="${VERSION_CODENAME:-}"
  [[ -n "$id" && -n "$codename" ]] || err "Cannot detect OS ID/VERSION_CODENAME from /etc/os-release."

  local version repo_codename component
  if ! read -r version repo_codename component < <(mongo_repo_defaults "$id" "$codename"); then
    err "MongoDB auto-install supports Debian bullseye/bookworm and Ubuntu jammy/noble. Detected: ${id} ${codename}. Install MongoDB manually or extend this script."
  fi

  log "Adding MongoDB ${version} apt repository for ${id} ${repo_codename}"
  local key_tmp keyring list_file repo_line
  key_tmp="$(mktemp)"
  keyring="/usr/share/keyrings/mongodb-server-${version}.gpg"
  curl -fsSL "https://pgp.mongodb.com/server-${version}.asc" | gpg --dearmor >"$key_tmp"
  "${SUDO[@]}" install -m 0644 "$key_tmp" "$keyring"
  rm -f "$key_tmp"

  if [[ "$id" == "ubuntu" ]]; then
    list_file="/etc/apt/sources.list.d/mongodb-org-${version}.list"
    repo_line="deb [ arch=amd64,arm64 signed-by=${keyring} ] https://repo.mongodb.org/apt/ubuntu ${repo_codename}/mongodb-org/${version} ${component}"
  else
    list_file="/etc/apt/sources.list.d/mongodb-org-${version}.list"
    repo_line="deb [ signed-by=${keyring} ] https://repo.mongodb.org/apt/debian ${repo_codename}/mongodb-org/${version} ${component}"
  fi

  printf '%s\n' "$repo_line" | "${SUDO[@]}" tee "$list_file" >/dev/null
  "${SUDO[@]}" apt-get update
  apt_install mongodb-org
  "${SUDO[@]}" systemctl enable --now mongod
}

log "Updating apt index"
"${SUDO[@]}" apt-get update

if [[ "${INSTALL_NODESOURCE:-0}" == "1" ]]; then
  # Need curl and ca-certificates before the NodeSource bootstrap.
  apt_install ca-certificates curl gnupg
  install_nodesource_repo
fi

log "Installing OS packages"
packages=(
  ca-certificates
  curl
  gnupg
  aria2
  ffmpeg
  webp
  python3
  python3-pip
  python3-venv
  nodejs
  git
  xz-utils
  unzip
  tar
)

if [[ "${INSTALL_NODESOURCE:-0}" != "1" ]]; then
  packages+=(npm)
fi

apt_install "${packages[@]}"

install_ytdlp_release
install_mongodb
install_vot_cli

log "Installed tool versions"
for cmd in ffmpeg ffprobe img2webp yt-dlp python3 node npm; do
  if command -v "$cmd" >/dev/null 2>&1; then
    case "$cmd" in
      ffmpeg|ffprobe) "$cmd" -version 2>/dev/null | head -n 1 ;;
      img2webp) "$cmd" -version 2>&1 | head -n 1 ;;
      *) "$cmd" --version 2>&1 | head -n 1 ;;
    esac
  else
    warn "$cmd not found"
  fi
done

if command -v node >/dev/null 2>&1; then
  major="$(node_major_version || true)"
  if [[ -n "$major" && "$major" -lt 18 ]]; then
    warn "Node.js ${major}.x is installed. vot-cli needs Node.js 18+. Re-run with INSTALL_NODESOURCE=1 NODE_MAJOR=22 if voice translate CLI is needed."
  fi
fi

if ! command -v lottie_to_webp >/dev/null 2>&1 && ! command -v lottie_to_webp.sh >/dev/null 2>&1; then
  cat <<'NOTE'
NOTE: animated custom-emoji preview for .tgs requires lottie_to_webp.
Install converter tools from:
  https://github.com/ed-asriyan/lottie-converter/releases
Make sure lottie_to_webp (or lottie_to_webp.sh) is available in PATH.
NOTE
fi

cat <<'DONE'

OK: dependencies installed.
Optional examples:
  INSTALL_MONGODB=1 ./scripts/install_deps.sh
  INSTALL_NODESOURCE=1 NODE_MAJOR=22 ./scripts/install_deps.sh
  INSTALL_VOT_CLI=1 ./scripts/install_deps.sh
DONE
