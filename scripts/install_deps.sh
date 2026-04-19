#!/usr/bin/env bash
set -euo pipefail

if ! command -v sudo >/dev/null 2>&1; then
  echo "sudo is required" >&2
  exit 1
fi

if ! command -v apt-get >/dev/null 2>&1; then
  echo "This installer supports apt-get only (Debian/Ubuntu)." >&2
  exit 1
fi

sudo apt-get update
sudo apt-get install -y ffmpeg webp aria2 curl gnupg

# MongoDB is optional; install only if MONGO_URI is set to a mongodb:// or if explicitly requested.
if [[ "${INSTALL_MONGODB:-}" == "1" ]]; then
  if [[ -f /etc/os-release ]]; then
    . /etc/os-release
    if [[ "${ID:-}" == "debian" && "${VERSION_CODENAME:-}" == "bullseye" ]]; then
      curl -fsSL https://pgp.mongodb.com/server-6.0.asc | sudo gpg -o /usr/share/keyrings/mongodb-server-6.0.gpg --dearmor
      echo "deb [ signed-by=/usr/share/keyrings/mongodb-server-6.0.gpg ] https://repo.mongodb.org/apt/debian bullseye/mongodb-org/6.0 main" | \
        sudo tee /etc/apt/sources.list.d/mongodb-org-6.0.list >/dev/null
      sudo apt-get update
      sudo apt-get install -y mongodb-org
      sudo systemctl enable --now mongod
    else
      echo "MongoDB auto-install only scripted for Debian bullseye. Set INSTALL_MONGODB=1 and install manually for this OS." >&2
      exit 1
    fi
  else
    echo "/etc/os-release not found; cannot auto-install MongoDB." >&2
    exit 1
  fi
fi

echo "OK: deps installed."
if ! command -v lottie_to_webp >/dev/null 2>&1 && ! command -v lottie_to_webp.sh >/dev/null 2>&1; then
  cat <<'EOF'
NOTE: animated custom-emoji preview for .tgs requires lottie_to_webp.
Install converter tools from:
  https://github.com/ed-asriyan/lottie-converter/releases
Make sure lottie_to_webp (or lottie_to_webp.sh) is available in PATH.
EOF
fi
