#!/bin/sh
set -eu

echo "This installs qvole from https://github.com/fernjager/qvole."
echo "By proceeding you agree to the privacy policy and terms of service:"
echo "  https://raw.githubusercontent.com/fernjager/qvole/main/docs/PRIVACY.md"
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

# The release asset filename (includes the .exe suffix for Windows). We download
# it verbatim so it matches the SHA256SUMS entry, then install as $BIN.
ARTIFACT="qvole-${OS}-${ARCH}"
[ "$OS" = "windows" ] && ARTIFACT="${ARTIFACT}.exe"

if [ "$VERSION" = "latest" ]; then
  BASE="https://github.com/${REPO}/releases/latest/download"
else
  BASE="https://github.com/${REPO}/releases/download/${VERSION}"
fi
URL="${BASE}/${ARTIFACT}"
SUM_URL="${BASE}/SHA256SUMS"

echo "Downloading qvole ${VERSION} for ${OS}/${ARCH}..."
TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT
curl -fsSLo "${TMPDIR}/${ARTIFACT}" "$URL"

# Verify the download against the release's SHA256SUMS before installing. The
# checksums file is produced by the same CI run that builds the binaries. When
# the file is present we fail closed on any mismatch or missing entry; only if
# it cannot be fetched at all (old tags predating checksums) do we warn and
# continue, so older releases keep working.
if curl -fsSLo "${TMPDIR}/SHA256SUMS" "$SUM_URL" 2>/dev/null; then
  expected=$(awk -v a="$ARTIFACT" '$2 == a {print $1}' "${TMPDIR}/SHA256SUMS")
  if [ -z "$expected" ]; then
    echo "error: no checksum listed for ${ARTIFACT} in SHA256SUMS" >&2
    exit 1
  fi
  if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "${TMPDIR}/${ARTIFACT}" | awk '{print $1}')
  elif command -v shasum >/dev/null 2>&1; then
    actual=$(shasum -a 256 "${TMPDIR}/${ARTIFACT}" | awk '{print $1}')
  else
    echo "error: no SHA-256 tool found (need sha256sum or shasum)" >&2
    exit 1
  fi
  if [ "$actual" != "$expected" ]; then
    echo "error: checksum mismatch for ${ARTIFACT}" >&2
    echo "  expected: ${expected}" >&2
    echo "  got:      ${actual}" >&2
    exit 1
  fi
  echo "Verified checksum for ${ARTIFACT}"
else
  echo "warning: could not fetch SHA256SUMS; skipping verification" >&2
fi

chmod +x "${TMPDIR}/${ARTIFACT}"

if [ -w "$INSTALL_DIR" ] || [ -w "$(dirname "$INSTALL_DIR")" ]; then
  mv "${TMPDIR}/${ARTIFACT}" "$INSTALL_DIR/${BIN}"
else
  echo "Need sudo to install to ${INSTALL_DIR}:"
  sudo mv "${TMPDIR}/${ARTIFACT}" "$INSTALL_DIR/${BIN}"
fi

if [ "$OS" = "darwin" ]; then
  xattr -dr com.apple.quarantine "$INSTALL_DIR/${BIN}" 2>/dev/null || true
  codesign -s - --deep --force "$INSTALL_DIR/${BIN}" 2>/dev/null || true
fi

echo "Installed to ${INSTALL_DIR}/${BIN}"
