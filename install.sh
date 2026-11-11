#!/usr/bin/env bash
# ddslop installer: downloads a released binary, verifies its checksum and
# optionally sets ddslop up for coding agents in the current repository.
#
#   curl -sSfL https://raw.githubusercontent.com/heikki-laitala/ddslop/main/install.sh | bash
#
# Non-interactive (flags skip the prompts):
#   curl -sSfL ... | bash -s -- --claude
#   curl -sSfL ... | bash -s -- --codex --copilot
#
# Options (pass after --):
#   --claude          run "ddslop init --claude" in the current repository
#   --codex           run "ddslop init --codex" in the current repository
#   --copilot         run "ddslop init --copilot" in the current repository
#   --dir DIR         install the binary to DIR instead of ~/.local/bin
#   --version V       install version V (default: latest)
#   --no-interactive  only install the binary

set -euo pipefail

REPO="heikki-laitala/ddslop"
INSTALL_DIR="${HOME}/.local/bin"
VERSION="latest"
INIT_ARGS=()
NO_INTERACTIVE=false

usage() {
    cat <<'USAGE'
Usage: install.sh [--claude] [--codex] [--copilot] [--dir DIR] [--version V] [--no-interactive]

  --claude          run "ddslop init --claude" in the current repository
  --codex           run "ddslop init --codex" in the current repository
  --copilot         run "ddslop init --copilot" in the current repository
  --dir DIR         install the binary to DIR (default: ~/.local/bin)
  --version V       install version V (default: latest)
  --no-interactive  only install the binary
USAGE
}

while [ $# -gt 0 ]; do
    case "$1" in
        --claude | --codex | --copilot) INIT_ARGS+=("$1") ;;
        --dir) INSTALL_DIR="$2"; shift ;;
        --version) VERSION="$2"; shift ;;
        --no-interactive) NO_INTERACTIVE=true ;;
        -h | --help) usage; exit 0 ;;
        *) echo "Unknown option: $1" >&2; exit 1 ;;
    esac
    shift
done

# ask reads an answer from the terminal, even when stdin is piped from curl.
ask() {
    local prompt="$1" default="$2" reply=""
    if [ -r /dev/tty ]; then
        printf "%s " "$prompt" > /dev/tty
        read -r reply < /dev/tty || reply=""
    fi
    echo "${reply:-$default}"
}

case "$(uname -s)" in
    Linux) OS_LABEL="linux" ;;
    Darwin) OS_LABEL="macos" ;;
    *) echo "Unsupported OS: $(uname -s). ddslop is released for Linux and macOS." >&2; exit 1 ;;
esac
case "$(uname -m)" in
    x86_64 | amd64) ARCH_LABEL="x86_64" ;;
    aarch64 | arm64) ARCH_LABEL="aarch64" ;;
    *) echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac
BINARY="ddslop-${OS_LABEL}-${ARCH_LABEL}"

if [ "$VERSION" = "latest" ]; then
    echo "Fetching the latest release..."
    # The web redirect names the latest tag without the API, whose limit for
    # requests without a login shared CI runners hit.
    latest=$(curl -sSfI -o /dev/null -w '%{redirect_url}' "https://github.com/${REPO}/releases/latest" || true)
    VERSION="${latest##*/tag/}"
    if [ -z "$latest" ] || [ "$VERSION" = "$latest" ]; then
        echo "Could not find the latest release. See https://github.com/${REPO}/releases" >&2
        exit 1
    fi
fi
echo "Installing ddslop ${VERSION} (${OS_LABEL}/${ARCH_LABEL})..."

BASE_URL="https://github.com/${REPO}/releases/download/${VERSION}"
TEMP_DIR=$(mktemp -d)
trap 'rm -rf "$TEMP_DIR"' EXIT

curl -sSfL "${BASE_URL}/${BINARY}.tar.gz" -o "${TEMP_DIR}/${BINARY}.tar.gz" || {
    echo "Download failed. See https://github.com/${REPO}/releases" >&2
    exit 1
}
curl -sSfL "${BASE_URL}/checksums.txt" -o "${TEMP_DIR}/checksums.txt" || {
    echo "Could not download checksums.txt for ${VERSION}" >&2
    exit 1
}

expected=$(grep " ${BINARY}.tar.gz\$" "${TEMP_DIR}/checksums.txt" | cut -d' ' -f1)
if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "${TEMP_DIR}/${BINARY}.tar.gz" | cut -d' ' -f1)
else
    actual=$(shasum -a 256 "${TEMP_DIR}/${BINARY}.tar.gz" | cut -d' ' -f1)
fi
if [ -z "$expected" ] || [ "$expected" != "$actual" ]; then
    echo "Checksum mismatch for ${BINARY}.tar.gz: expected '${expected}', got '${actual}'" >&2
    exit 1
fi

tar -xzf "${TEMP_DIR}/${BINARY}.tar.gz" -C "$TEMP_DIR"
mkdir -p "$INSTALL_DIR"
cp "${TEMP_DIR}/${BINARY}/ddslop" "${INSTALL_DIR}/ddslop"
chmod +x "${INSTALL_DIR}/ddslop"
echo "Installed ${INSTALL_DIR}/ddslop"
"${INSTALL_DIR}/ddslop" version

if ! echo "$PATH" | tr ':' '\n' | grep -qx "$INSTALL_DIR"; then
    echo ""
    echo "WARNING: ${INSTALL_DIR} is not in your PATH. Add this to ~/.zshrc or ~/.bashrc:"
    echo ""
    echo "  export PATH=\"${INSTALL_DIR}:\$PATH\""
fi

# ddslop is set up per repository, so setup only applies inside one.
if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    if [ ${#INIT_ARGS[@]} -gt 0 ]; then
        echo "Not inside a Git repository: run 'ddslop init ${INIT_ARGS[*]}' in your project." >&2
    fi
    exit 0
fi
ROOT=$(git rev-parse --show-toplevel)

if [ ${#INIT_ARGS[@]} -eq 0 ] && [ "$NO_INTERACTIVE" = false ] && [ -r /dev/tty ]; then
    echo ""
    echo "Set ddslop up for coding agents in ${ROOT}?"
    echo ""
    echo "  1) Claude Code"
    echo "  2) Codex"
    echo "  3) Copilot"
    echo "  4) All three"
    echo "  5) Skip: run 'ddslop init' later"
    echo ""
    case "$(ask "Choice [1]:" "1")" in
        1) INIT_ARGS=(--claude) ;;
        2) INIT_ARGS=(--codex) ;;
        3) INIT_ARGS=(--copilot) ;;
        4) INIT_ARGS=(--claude --codex --copilot) ;;
        *) echo "Skipped. Run 'ddslop init' in your project when ready."; exit 0 ;;
    esac
fi

if [ ${#INIT_ARGS[@]} -gt 0 ]; then
    echo ""
    "${INSTALL_DIR}/ddslop" init "${INIT_ARGS[@]}" "$ROOT"
fi
echo ""
echo "Done. Run 'ddslop check' to find duplicated code."
