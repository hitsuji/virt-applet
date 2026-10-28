# kvm-applet

A system tray applet for listing and controlling libvirt/QEMU virtual machines
on the local host and on remote hosts over SSH.

Each configured connection gets a section in the tray menu showing its VMs and
their state. Each VM has a submenu with the actions that make sense for its
current state:

- Start, Resume, Shut down, Reboot, Pause
- Save state and stop (libvirt managed save)
- Open console (in virt-manager)
- Force reset, Force off
- Start on host boot (autostart toggle)

The menu updates from libvirt lifecycle events, with polling as a fallback,
and reconnects automatically if a host goes away. The tray icon shows whether
any VM is running, or whether a connection has failed. Errors are shown as
desktop notifications.

## Requirements

- A desktop with a StatusNotifierItem tray. On Ubuntu's GNOME session this is
  `gnome-shell-extension-appindicator`, which is installed by default.
- Access to libvirt. For `qemu:///system`, your user must be in the `libvirt`
  group.
- Optional: `virt-manager` for consoles, `libnotify-bin` for error
  notifications, and `openssh-client` for remote hosts.

## Install

### Debian package (Ubuntu)

```sh
./build-deb.sh 0.1.0        # or: make deb VERSION=0.1.0
sudo apt install ./dist/kvm-applet_0.1.0_amd64.deb
systemctl --user enable --now kvm-applet
```

Set `GOARCH` to build for another architecture, e.g.
`GOARCH=arm64 ./build-deb.sh`.

### From source, per user

```sh
make install                # installs to ~/.local/bin
systemctl --user enable --now kvm-applet
```

`make uninstall` removes it again. Building needs Go 1.27 or later.

## Configuration

Connections are read from `~/.config/kvm-applet/connections` (or
`$XDG_CONFIG_HOME/kvm-applet/connections`). Each line is a libvirt URI,
optionally preceded by a display name:

```
local    qemu:///system
nas      qemu+ssh://admin@nas.lan/system
session  qemu:///session
```

The name defaults to the URI's hostname, or `local` / `session`. Names must be
unique. Lines starting with `#` are comments, and `$VARS` in URIs are expanded
from the environment. See `connections.example` (installed by the package to
`/usr/share/doc/kvm-applet/examples/connections`). Restart the applet after
editing.

If the file is missing or empty, the applet uses `$LIBVIRT_DEFAULT_URI`, or
`qemu:///system` if that is unset.

### Remote hosts

`qemu+ssh://` URIs use your system `ssh`, so `~/.ssh/config` aliases, keys and
`ProxyJump` all work. Authentication must not prompt: use keys or an agent.
The URI may include a port and `?keyfile=` and `?socket=` parameters. The
remote host needs `virt-ssh-helper` (libvirt 6.8 or later) or `nc` with Unix
socket support.

go-libvirt's built-in SSH client is still available as `qemu+libssh://`.

### Command-line options

```
-uri URI          libvirt URI to manage; repeatable, overrides the config file
-config PATH      connections file (default ~/.config/kvm-applet/connections)
-interval DUR     poll / reconnect interval (default 5s)
```

## Logs

When run as a systemd user service:

```sh
journalctl --user -u kvm-applet -f
```

## Licence

MIT; see [LICENSE](LICENSE). The binary includes third-party Go modules under
Apache-2.0 and BSD licences; the Debian package lists them in
`/usr/share/doc/kvm-applet/copyright`.
