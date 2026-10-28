// virt-applet is a system tray applet for listing and controlling libvirt/QEMU VMs.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"fyne.io/systray"
	"github.com/digitalocean/go-libvirt"
)

type action struct {
	label string
	show  func(libvirt.DomainState) bool
	run   func(c *conn, vm VM) error
}

func isOff(s libvirt.DomainState) bool {
	return s == libvirt.DomainShutoff || s == libvirt.DomainCrashed || s == libvirt.DomainNostate
}
func isRunning(s libvirt.DomainState) bool {
	return s == libvirt.DomainRunning || s == libvirt.DomainBlocked
}
func isPaused(s libvirt.DomainState) bool { return s == libvirt.DomainPaused }
func isOn(s libvirt.DomainState) bool     { return !isOff(s) }
func either(f, g func(libvirt.DomainState) bool) func(libvirt.DomainState) bool {
	return func(s libvirt.DomainState) bool { return f(s) || g(s) }
}

// lv adapts a plain libvirt domain call into an action.
func lv(fn func(*libvirt.Libvirt, libvirt.Domain) error) func(*conn, VM) error {
	return func(c *conn, vm VM) error {
		return c.client.Do(func(l *libvirt.Libvirt) error { return fn(l, vm.Dom) })
	}
}

var actions = []action{
	{"Start", isOff, lv((*libvirt.Libvirt).DomainCreate)},
	{"Resume", isPaused, lv((*libvirt.Libvirt).DomainResume)},
	{"Shut down", isRunning, lv((*libvirt.Libvirt).DomainShutdown)},
	{"Reboot", isRunning, lv(func(l *libvirt.Libvirt, d libvirt.Domain) error {
		return l.DomainReboot(d, 0)
	})},
	{"Pause", isRunning, lv((*libvirt.Libvirt).DomainSuspend)},
	{"Save state and stop", either(isRunning, isPaused), lv(func(l *libvirt.Libvirt, d libvirt.Domain) error {
		return l.DomainManagedSave(d, 0)
	})},
	{"Open console", isOn, func(c *conn, vm VM) error {
		return launch("virt-manager", "--connect", c.URI.String(), "--show-domain-console", vm.Dom.Name)
	}},
	{"Force reset", isRunning, lv(func(l *libvirt.Libvirt, d libvirt.Domain) error {
		return l.DomainReset(d, 0)
	})},
	{"Force off", isOn, lv((*libvirt.Libvirt).DomainDestroy)},
}

func stateLabel(s libvirt.DomainState) (glyph, text string) {
	switch s {
	case libvirt.DomainRunning:
		return "●", "running"
	case libvirt.DomainBlocked:
		return "●", "blocked"
	case libvirt.DomainPaused:
		return "⏸", "paused"
	case libvirt.DomainShutdown:
		return "◐", "shutting down"
	case libvirt.DomainShutoff:
		return "○", "shut off"
	case libvirt.DomainCrashed:
		return "✖", "crashed"
	case libvirt.DomainPmsuspended:
		return "☾", "suspended"
	default:
		return "?", "unknown"
	}
}

// menuItem wraps a systray item and caches what was last sent over D-Bus,
// so periodic refreshes don't spam the tray host with identical updates.
type menuItem struct {
	*systray.MenuItem
	title   string
	hidden  bool
	checked bool
}

func (m *menuItem) setTitle(t string) {
	if m.title != t {
		m.title = t
		m.SetTitle(t)
	}
}

func (m *menuItem) setVisible(v bool) {
	if m.hidden == !v {
		return
	}
	m.hidden = !v
	if v {
		m.Show()
	} else {
		m.Hide()
	}
}

func (m *menuItem) setChecked(c bool) {
	if m.checked != c {
		m.checked = c
		if c {
			m.Check()
		} else {
			m.Uncheck()
		}
	}
}

type vmMenu struct {
	root      *menuItem
	actions   []*menuItem
	autostart *menuItem
}

// conn is one configured libvirt target and its latest state.
type conn struct {
	connConfig
	client *Client
	snap   Snapshot
	seen   bool // received at least one snapshot
	header *menuItem
}

func (c *conn) vmKey(vm VM) string { return c.Name + "/" + vm.Key() }

func (c *conn) status() string {
	switch {
	case !c.seen:
		return "connecting…"
	case c.snap.Err != nil:
		return "⚠ " + truncate(c.snap.Err.Error(), 60)
	}
	running := 0
	for _, vm := range c.snap.VMs {
		if isOn(vm.State) {
			running++
		}
	}
	return fmt.Sprintf("%d of %d running", running, len(c.snap.VMs))
}

type update struct {
	c    *conn
	snap Snapshot
}

type vmRef struct {
	c  *conn
	vm VM
}

type app struct {
	conns   []*conn
	updates chan update
	cancel  context.CancelFunc

	menus  map[string]*vmMenu
	layout []string
	icon   []byte

	mu      sync.Mutex
	current map[string]vmRef
}

func newApp(cfgs []connConfig, interval time.Duration) *app {
	a := &app{updates: make(chan update)}
	for _, cfg := range cfgs {
		a.conns = append(a.conns, &conn{connConfig: cfg, client: NewClient(cfg.URI, interval)})
	}
	return a
}

func (a *app) onReady() {
	systray.SetTitle("KVM")
	a.render()

	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	for _, c := range a.conns {
		go c.client.Run(ctx)
		go func() {
			for s := range c.client.Updates() {
				a.updates <- update{c, s}
			}
		}()
	}
	go func() {
		for u := range a.updates {
			u.c.snap, u.c.seen = u.snap, true
			a.render()
		}
	}()
}

func (a *app) onExit() {
	if a.cancel != nil {
		a.cancel()
	}
}

func (a *app) setIcon(icon []byte) {
	if !slices.Equal(a.icon, icon) {
		a.icon = icon
		systray.SetIcon(icon)
	}
}

// onClick runs fn in the background each time the item is clicked. The loop
// ends when the item is removed, since systray closes ClickedCh then.
func onClick(m *systray.MenuItem, fn func()) {
	go func() {
		for range m.ClickedCh {
			go fn()
		}
	}()
}

// layoutOf describes the menu structure; the menu is only rebuilt when it
// changes, since systray can't insert items in the middle of a menu.
func (a *app) layoutOf() []string {
	var l []string
	for _, c := range a.conns {
		l = append(l, "#"+c.Name)
		if c.seen && c.snap.Err == nil && len(c.snap.VMs) == 0 {
			l = append(l, "empty")
		}
		for _, vm := range c.snap.VMs {
			l = append(l, c.vmKey(vm))
		}
	}
	return l
}

func (a *app) rebuild(layout []string) {
	systray.ResetMenu()
	a.layout = layout
	a.menus = map[string]*vmMenu{}

	for i, c := range a.conns {
		if i > 0 {
			systray.AddSeparator()
		}
		c.header = &menuItem{MenuItem: systray.AddMenuItem("", "")}
		c.header.Disable()
		if c.seen && c.snap.Err == nil && len(c.snap.VMs) == 0 {
			systray.AddMenuItem("    No virtual machines", "").Disable()
		}
		for _, vm := range c.snap.VMs {
			key := c.vmKey(vm)
			m := &vmMenu{root: &menuItem{MenuItem: systray.AddMenuItem(vm.Dom.Name, "")}}
			for _, act := range actions {
				item := &menuItem{MenuItem: m.root.AddSubMenuItem(act.label, ""), title: act.label}
				m.actions = append(m.actions, item)
				onClick(item.MenuItem, func() { a.perform(key, act) })
			}
			m.root.AddSeparator()
			m.autostart = &menuItem{MenuItem: m.root.AddSubMenuItemCheckbox("Start on host boot", "", false)}
			onClick(m.autostart.MenuItem, func() { a.toggleAutostart(key) })
			a.menus[key] = m
		}
	}

	systray.AddSeparator()
	vmm := systray.AddMenuItem("Open Virtual Machine Manager", "")
	if len(a.conns) == 1 {
		onClick(vmm, func() { openManager(a.conns[0]) })
	} else {
		for _, c := range a.conns {
			onClick(vmm.AddSubMenuItem(c.Name, c.URI.Redacted()), func() { openManager(c) })
		}
	}
	onClick(systray.AddMenuItem("Refresh", ""), func() {
		for _, c := range a.conns {
			c.client.Refresh()
		}
	})
	onClick(systray.AddMenuItem("Quit", ""), systray.Quit)
}

func (a *app) render() {
	current := map[string]vmRef{}
	for _, c := range a.conns {
		for _, vm := range c.snap.VMs {
			current[c.vmKey(vm)] = vmRef{c, vm}
		}
	}
	a.mu.Lock()
	a.current = current
	a.mu.Unlock()

	if layout := a.layoutOf(); a.menus == nil || !slices.Equal(layout, a.layout) {
		a.rebuild(layout)
	}

	var tooltip []string
	running, failed := false, false
	for _, c := range a.conns {
		line := fmt.Sprintf("%s — %s", c.Name, c.status())
		c.header.setTitle(line)
		tooltip = append(tooltip, line)
		failed = failed || c.snap.Err != nil

		for _, vm := range c.snap.VMs {
			running = running || isOn(vm.State)
			glyph, text := stateLabel(vm.State)
			m := a.menus[c.vmKey(vm)]
			m.root.setTitle(fmt.Sprintf("    %s  %s — %s", glyph, vm.Dom.Name, text))
			for i, act := range actions {
				m.actions[i].setVisible(act.show(vm.State))
			}
			m.autostart.setChecked(vm.Autostart)
		}
	}
	systray.SetTooltip("KVM\n" + strings.Join(tooltip, "\n"))

	switch {
	case running:
		a.setIcon(iconRunning)
	case failed:
		a.setIcon(iconError)
	default:
		a.setIcon(iconIdle)
	}
}

func (a *app) lookup(key string) (vmRef, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r, ok := a.current[key]
	return r, ok
}

func (a *app) perform(key string, act action) {
	r, ok := a.lookup(key)
	if !ok {
		return
	}
	log.Printf("%s/%s: %s", r.c.Name, r.vm.Dom.Name, act.label)
	if err := act.run(r.c, r.vm); err != nil {
		notify(fmt.Sprintf("%s: %s failed", r.vm.Dom.Name, act.label), err)
	}
}

func (a *app) toggleAutostart(key string) {
	r, ok := a.lookup(key)
	if !ok {
		return
	}
	want := int32(1)
	if r.vm.Autostart {
		want = 0
	}
	err := r.c.client.Do(func(l *libvirt.Libvirt) error { return l.DomainSetAutostart(r.vm.Dom, want) })
	if err != nil {
		notify(r.vm.Dom.Name+": changing autostart failed", err)
	}
}

func openManager(c *conn) {
	if err := launch("virt-manager", "--connect", c.URI.String()); err != nil {
		notify("Could not open Virtual Machine Manager", err)
	}
}

// launch starts a detached GUI helper and reaps it when it exits.
func launch(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

func notify(summary string, err error) {
	log.Printf("%s: %v", summary, err)
	_ = exec.Command("notify-send", "-a", "KVM", "-i", "dialog-error", summary, err.Error()).Run()
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func main() {
	var uris []string
	flag.Func("uri", "libvirt URI to manage; repeatable, overrides the config file", func(s string) error {
		uris = append(uris, s)
		return nil
	})
	configPath := flag.String("config", defaultConfigPath(), "connections file")
	interval := flag.Duration("interval", 5*time.Second, "poll / reconnect interval")
	flag.Parse()

	// Precedence: -uri flags, then the config file, then LIBVIRT_DEFAULT_URI
	// or qemu:///system.
	var cfgs []connConfig
	if len(uris) == 0 {
		var err error
		if cfgs, err = loadConfig(*configPath); err != nil {
			log.Fatal(err)
		}
		if len(cfgs) == 0 {
			if u := os.Getenv("LIBVIRT_DEFAULT_URI"); u != "" {
				uris = []string{u}
			} else {
				uris = []string{"qemu:///system"}
			}
		}
	}
	for _, raw := range uris {
		c, err := parseConn("", strings.TrimSpace(raw))
		if err != nil {
			log.Fatalf("invalid -uri: %v", err)
		}
		cfgs = append(cfgs, c)
	}

	seen := map[string]bool{}
	for _, c := range cfgs {
		if seen[c.Name] {
			log.Fatalf("duplicate connection name %q; give each one a unique name", c.Name)
		}
		seen[c.Name] = true
	}

	a := newApp(cfgs, *interval)
	systray.Run(a.onReady, a.onExit)
}
