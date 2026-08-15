#!/bin/sh
# SmartEfficiency installer for Linux and macOS.
#
# Downloads the latest release for this machine's OS/architecture from
# GitHub, installs it to the same per-OS directory the binaries themselves
# use (see internal/config/dir_linux.go / dir_darwin.go), and registers
# autostart via systemd --user (Linux) or launchd (macOS) - neither needs
# root/sudo, unlike the Windows installer which needs one UAC prompt for
# Task Scheduler.
#
# NOTE on platform maturity: the Linux and macOS backends were written
# against each OS's documented APIs but were NOT run on real Linux/macOS
# hardware while building this project (only Windows was available to test
# on). They should work, but please open an issue if something doesn't -
# see README.md for the exact list of what's verified vs. not per platform.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/YoMosa2009/SmartEfficiency/main/install.sh | sh

set -e

REPO="YoMosa2009/SmartEfficiency"

os_name() {
    case "$(uname -s)" in
        Linux) echo "linux" ;;
        Darwin) echo "darwin" ;;
        *) echo "unsupported"; return 1 ;;
    esac
}

arch_name() {
    case "$(uname -m)" in
        x86_64|amd64) echo "amd64" ;;
        aarch64|arm64) echo "arm64" ;;
        *) echo "unsupported"; return 1 ;;
    esac
}

OS="$(os_name)"
ARCH="$(arch_name)"

if [ "$OS" = "unsupported" ] || [ "$ARCH" = "unsupported" ]; then
    echo "SmartEfficiency: unsupported OS/architecture ($(uname -s) $(uname -m))" >&2
    exit 1
fi

if [ "$OS" = "linux" ]; then
    INSTALL_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/smartefficiency"
else
    INSTALL_DIR="$HOME/Library/Application Support/SmartEfficiency"
fi

echo "SmartEfficiency installer"
echo "  Platform:    $OS-$ARCH"
echo "  Install dir: $INSTALL_DIR"

mkdir -p "$INSTALL_DIR"

echo "Fetching latest release info..."
RELEASE_JSON="$(curl -fsSL -H "User-Agent: SmartEfficiency-installer" "https://api.github.com/repos/$REPO/releases/latest")"
TAG="$(printf '%s' "$RELEASE_JSON" | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')"
echo "  Latest version: $TAG"

download_asset() {
    name="$1"
    dest="$2"
    url="$(printf '%s' "$RELEASE_JSON" | grep -o "\"browser_download_url\": *\"[^\"]*${name}\"" | sed -E 's/.*"(https[^"]+)"/\1/')"
    if [ -z "$url" ]; then
        echo "No release asset named '$name' found in the latest release." >&2
        exit 1
    fi
    echo "  Downloading $name ..."
    curl -fsSL -o "$dest" "$url"
    chmod +x "$dest"
}

download_asset "smarteffd-$OS-$ARCH"       "$INSTALL_DIR/smarteffd"
download_asset "smarteff-tray-$OS-$ARCH"   "$INSTALL_DIR/smarteff-tray"
download_asset "smarteff-update-$OS-$ARCH" "$INSTALL_DIR/smarteff-update"

echo "Registering autostart..."
if [ "$OS" = "linux" ]; then
    echo "  (systemd --user service - no sudo needed)"
else
    echo "  (launchd agent - no sudo needed)"
fi
"$INSTALL_DIR/smarteffd" -install

echo ""
echo "Installed SmartEfficiency $TAG."
echo "The daemon is running now and will start automatically at every login."
if [ "$OS" = "linux" ]; then
    echo "Tray icon requires a running graphical session (X11 or Wayland+XWayland for focus"
    echo "detection specifically - see README's Linux notes)."
fi
echo "To remove the autostart registration later, run:"
echo "  \"$INSTALL_DIR/smarteffd\" -uninstall"
