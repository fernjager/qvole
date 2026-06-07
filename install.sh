#!/bin/sh
set -eu

echo "This installs qvole from https://github.com/fernjager/qvole."
echo "By proceeding you agree to the privacy policy and terms of service:"
echo "  https://raw.githubusercontent.com/fernjager/qvole/main/PRIVACY.md"
echo

REPO="fernjager/qvole"
VERSION="${VERSION:-latest}"
BIN="${BIN:-qvole}"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"

usage() {
  cat <<EOF
Usage: curl -fsSL https://raw.githubusercontent.com/${REPO}/main/install.sh | sh

Environment:
  VERSION       release tag (default: latest)
  BIN           binary name (default: qvole)
  INSTALL_DIR   install path (default: /usr/local/bin)
EOF
  exit 0
}

case "${1:-}" in
  -h|--help) usage ;;
esac

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
  linux|darwin|freebsd) ;;
  *) echo "Unsupported OS: $OS"; exit 1 ;;
esac

ARCH=$(uname -m)
case "$ARCH" in
  x86_64)  ARCH="amd64" ;;
  aarch64) ARCH="arm64" ;;
  arm64)   ;;
  armv7l)  ARCH="arm" ;;
  armv6l)  ARCH="arm" ;;
  mips)    ARCH="mips"    ;;
  mipsel)  ARCH="mipsle"  ;;
  mips64)  ARCH="mips64"  ;;
  mips64el) ARCH="mips64le" ;;
  *) echo "Unsupported arch: $ARCH"; exit 1 ;;
esac

if [ "$VERSION" = "latest" ]; then
  URL="https://github.com/${REPO}/releases/latest/download/qvole-${OS}-${ARCH}"
else
  URL="https://github.com/${REPO}/releases/download/${VERSION}/qvole-${OS}-${ARCH}"
fi

echo "Downloading qvole ${VERSION} for ${OS}/${ARCH}..."
curl -fsSLo /tmp/${BIN} "$URL"
chmod +x /tmp/${BIN}

if [ -w "$INSTALL_DIR" ] || [ -w "$(dirname "$INSTALL_DIR")" ]; then
  mv /tmp/${BIN} "$INSTALL_DIR/${BIN}"
else
  echo "Need sudo to install to ${INSTALL_DIR}:"
  sudo mv /tmp/${BIN} "$INSTALL_DIR/${BIN}"
fi

if [ "$OS" = "darwin" ]; then
  xattr -dr com.apple.quarantine "$INSTALL_DIR/${BIN}" 2>/dev/null || true
  codesign -s - --deep --force "$INSTALL_DIR/${BIN}" 2>/dev/null || true
fi

echo "Installed to ${INSTALL_DIR}/${BIN}"
