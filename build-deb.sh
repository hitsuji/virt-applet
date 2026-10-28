#!/usr/bin/env bash
# Build a .deb of virt-applet for Ubuntu.
#
# Usage: ./build-deb.sh [version]        (default: 0.1.0)
#   GOARCH=arm64 ./build-deb.sh 0.2.0    cross-build for another arch
#   DEBFULLNAME / DEBEMAIL               override the Maintainer field
set -euo pipefail
umask 022

cd "$(dirname "$0")"

PKG=virt-applet
VERSION=${1:-0.1.0}
export GOOS=linux GOARCH=${GOARCH:-$(go env GOARCH)} CGO_ENABLED=0
DEFAULT_NAME="Shane O'Sullivan"
MAINTAINER="${DEBFULLNAME:-$DEFAULT_NAME} <${DEBEMAIL:-shane@osullivan.cc}>"

# Go and Debian mostly agree on arch names; map the ones that differ.
case "$GOARCH" in
	arm)  DEB_ARCH=armhf ;;
	386)  DEB_ARCH=i386 ;;
	*)    DEB_ARCH=$GOARCH ;;
esac

OUT=dist
ROOT=$(mktemp -d)
trap 'rm -rf "$ROOT"' EXIT
chmod 755 "$ROOT"

echo "==> Building $PKG $VERSION ($DEB_ARCH)"
go build -trimpath -ldflags="-s -w" -o "$ROOT/usr/bin/$PKG" .

# User unit: same as the Makefile's, but pointing at the system binary.
install -d "$ROOT/usr/lib/systemd/user"
sed 's|^ExecStart=.*|ExecStart=/usr/bin/virt-applet|' virt-applet.service \
	> "$ROOT/usr/lib/systemd/user/$PKG.service"

install -Dm644 connections.example "$ROOT/usr/share/doc/$PKG/examples/connections"

# The binary is statically linked, so it carries the Go runtime and every
# module it imports; their licences must ship alongside ours.
{
	echo "virt-applet"
	echo
	cat LICENSE
	echo
	echo "--------------------------------------------------------------------"
	echo "Go standard library and runtime"
	echo
	cat "$(go env GOROOT)/LICENSE"
	go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}} {{.Dir}}{{end}}{{end}}' . |
		sort -u |
		while read -r path version dir; do
			echo
			echo "--------------------------------------------------------------------"
			echo "$path $version"
			echo
			cat "$dir"/LICENSE*
		done
} > "$ROOT/usr/share/doc/$PKG/copyright"

install -d "$ROOT/DEBIAN"
cat > "$ROOT/DEBIAN/control" <<EOF
Package: $PKG
Version: $VERSION
Architecture: $DEB_ARCH
Maintainer: $MAINTAINER
Section: admin
Priority: optional
Installed-Size: $(du -sk --exclude=DEBIAN "$ROOT" | cut -f1)
Recommends: virt-manager, openssh-client, libnotify-bin, gnome-shell-extension-appindicator
Suggests: libvirt-daemon-system
Description: System tray applet for KVM/libvirt virtual machines
 Lists libvirt domains on local and remote (qemu+ssh) hosts in a tray
 menu, and opens their consoles in virt-manager.
 .
 Enable per user with: systemctl --user enable --now virt-applet
EOF

cat > "$ROOT/DEBIAN/postinst" <<'EOF'
#!/bin/sh
set -e
if [ "$1" = "configure" ]; then
	echo "virt-applet: enable at login with: systemctl --user enable --now virt-applet"
	echo "virt-applet: example config in /usr/share/doc/virt-applet/examples/connections"
fi
EOF
chmod 755 "$ROOT/DEBIAN/postinst"

mkdir -p "$OUT"
DEB="$OUT/${PKG}_${VERSION}_${DEB_ARCH}.deb"
dpkg-deb --root-owner-group --build "$ROOT" "$DEB"

echo "==> Built $DEB"
echo "    Install with: sudo apt install ./$DEB"
