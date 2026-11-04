#!/bin/sh
# Installs Ferry for the current user.
set -e
cd "$(dirname "$0")"
mkdir -p "$HOME/.local/bin" "$HOME/.local/share/applications" "$HOME/.local/share/icons/hicolor/256x256/apps"
install -m 755 ferry "$HOME/.local/bin/ferry"
install -m 644 ferry.png "$HOME/.local/share/icons/hicolor/256x256/apps/ferry.png"
install -m 644 ferry.desktop "$HOME/.local/share/applications/ferry.desktop"
echo "Ferry installed. Find it in your app menu, or run: ferry"
echo "Tip: to use port 80 (so others can open http://ferry.local without a port), run once:"
echo "  sudo setcap cap_net_bind_service=+ep $HOME/.local/bin/ferry"
