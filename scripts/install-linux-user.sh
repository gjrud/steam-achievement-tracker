#!/usr/bin/env sh
set -eu

APP_ID="steam-achievement-tracker"
APP_NAME="Steam Achievement Tracker"
ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
BINARY_TARGET="$HOME/.local/bin/$APP_ID"
DESKTOP_FILE="$HOME/.local/share/applications/$APP_ID.desktop"
ICON_SCALABLE_DIR="$HOME/.local/share/icons/hicolor/scalable/apps"
ICON_PNG_DIR="$HOME/.local/share/icons/hicolor/1024x1024/apps"

usage() {
	cat <<EOF
Usage:
  $0 install [binary]
  $0 uninstall

Defaults:
  install binary: $ROOT_DIR/build/bin/$APP_ID

Uninstall removes app binary, desktop entry, and icons.
It does not remove ~/.steam-achievement-tracker or Secret Service keys.
EOF
}

refresh_caches() {
	if command -v gtk-update-icon-cache >/dev/null 2>&1; then
		gtk-update-icon-cache -q "$HOME/.local/share/icons/hicolor" || true
	fi

	if command -v update-desktop-database >/dev/null 2>&1; then
		update-desktop-database -q "$HOME/.local/share/applications" || true
	fi
}

install_app() {
	BINARY_SOURCE="${1:-$ROOT_DIR/build/bin/$APP_ID}"

	if [ ! -f "$BINARY_SOURCE" ]; then
		printf '%s\n' "Missing binary: $BINARY_SOURCE" >&2
		printf '%s\n' "Run: wails build" >&2
		exit 1
	fi

	mkdir -p "$HOME/.local/bin" "$HOME/.local/share/applications" "$ICON_SCALABLE_DIR" "$ICON_PNG_DIR"

	install -m 0755 "$BINARY_SOURCE" "$BINARY_TARGET"
	install -m 0644 "$ROOT_DIR/build/appicon.svg" "$ICON_SCALABLE_DIR/$APP_ID.svg"
	install -m 0644 "$ROOT_DIR/build/appicon.png" "$ICON_PNG_DIR/$APP_ID.png"

	cat > "$DESKTOP_FILE" <<EOF
[Desktop Entry]
Type=Application
Name=$APP_NAME
Comment=Track Steam achievement completion
Exec=$BINARY_TARGET
Icon=$APP_ID
Terminal=false
Categories=Game;Utility;
StartupNotify=true
StartupWMClass=$APP_ID
EOF

	chmod 0644 "$DESKTOP_FILE"
	refresh_caches

	printf '%s\n' "Installed: $BINARY_TARGET"
	printf '%s\n' "Desktop entry: $DESKTOP_FILE"
	printf '%s\n' "Launch from app menu once caches refresh."
}

uninstall_app() {
	rm -f "$BINARY_TARGET"
	rm -f "$DESKTOP_FILE"
	rm -f "$ICON_SCALABLE_DIR/$APP_ID.svg"
	rm -f "$ICON_PNG_DIR/$APP_ID.png"
	refresh_caches

	printf '%s\n' "Uninstalled app files."
	printf '%s\n' "Kept app data: $HOME/.steam-achievement-tracker"
	printf '%s\n' "Kept Secret Service key: service=$APP_ID username=steam-web-api-key"
}

COMMAND="${1:-install}"

case "$COMMAND" in
	install)
		if [ "$#" -gt 0 ]; then
			shift
		fi
		install_app "${1:-}"
		;;
	uninstall)
		uninstall_app
		;;
	-h|--help|help)
		usage
		;;
	*)
		if [ -f "$COMMAND" ]; then
			install_app "$COMMAND"
		else
			usage >&2
			exit 2
		fi
		;;
esac
