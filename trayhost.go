package main

import (
	"context"
	"strings"

	"github.com/godbus/dbus/v5"
)

// watchTrayHosts calls fn whenever a StatusNotifier host appears on the
// session bus, e.g. when waybar recreates its bar after a monitor hotplug.
//
// A host that connects after the menu was built fetches the whole tree in one
// go, and libdbusmenu-gtk then drops the children of some submenus (they show
// up blank). systray only works around this for the first fetch after
// ResetMenu, so the caller should rebuild the menu when fn fires.
func watchTrayHosts(ctx context.Context, fn func()) error {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return err
	}
	err = conn.AddMatchSignal(
		dbus.WithMatchSender("org.freedesktop.DBus"),
		dbus.WithMatchInterface("org.freedesktop.DBus"),
		dbus.WithMatchMember("NameOwnerChanged"),
	)
	if err != nil {
		conn.Close()
		return err
	}
	signals := make(chan *dbus.Signal, 16)
	conn.Signal(signals)

	go func() {
		defer conn.Close()
		for {
			select {
			case <-ctx.Done():
				return
			case s, ok := <-signals:
				if !ok {
					return
				}
				var name, oldOwner, newOwner string
				if dbus.Store(s.Body, &name, &oldOwner, &newOwner) != nil || newOwner == "" {
					continue
				}
				if strings.HasPrefix(name, "org.kde.StatusNotifierHost-") ||
					strings.HasPrefix(name, "org.freedesktop.StatusNotifierHost-") {
					fn()
				}
			}
		}
	}()
	return nil
}
