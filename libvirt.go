package main

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/digitalocean/go-libvirt"
)

// VM is a point-in-time view of one libvirt domain.
type VM struct {
	Dom       libvirt.Domain
	State     libvirt.DomainState
	Autostart bool
}

func (v VM) Key() string { return fmt.Sprintf("%x", v.Dom.UUID) }

// Snapshot is what the UI renders: either a VM list or a connection error.
type Snapshot struct {
	VMs []VM
	Err error
}

// Client keeps a libvirt connection alive and publishes snapshots whenever
// something changes (lifecycle event, poll tick, or an explicit refresh).
type Client struct {
	uri      *url.URL
	interval time.Duration
	updates  chan Snapshot
	refresh  chan struct{}

	mu sync.Mutex
	lv *libvirt.Libvirt
}

func NewClient(uri *url.URL, interval time.Duration) *Client {
	return &Client{
		uri:      uri,
		interval: interval,
		updates:  make(chan Snapshot, 1),
		refresh:  make(chan struct{}, 1),
	}
}

func (c *Client) Updates() <-chan Snapshot { return c.updates }

// Refresh requests a new snapshot without blocking.
func (c *Client) Refresh() {
	select {
	case c.refresh <- struct{}{}:
	default:
	}
}

func (c *Client) conn() (*libvirt.Libvirt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lv == nil || !c.lv.IsConnected() {
		return nil, fmt.Errorf("not connected to %s", c.uri)
	}
	return c.lv, nil
}

// publish replaces any unconsumed snapshot so the UI always gets the latest.
func (c *Client) publish(s Snapshot) {
	select {
	case <-c.updates:
	default:
	}
	c.updates <- s
}

// Run connects, subscribes to lifecycle events, and reconnects on failure.
func (c *Client) Run(ctx context.Context) {
	for ctx.Err() == nil {
		lv, err := connect(c.uri)
		if err != nil {
			log.Printf("connect %s: %v", c.uri, err)
			c.publish(Snapshot{Err: err})
			c.sleep(ctx)
			continue
		}
		c.mu.Lock()
		c.lv = lv
		c.mu.Unlock()

		c.watch(ctx, lv)

		c.mu.Lock()
		c.lv = nil
		c.mu.Unlock()
		lv.Disconnect()
	}
}

func (c *Client) sleep(ctx context.Context) {
	select {
	case <-ctx.Done():
	case <-time.After(c.interval):
	case <-c.refresh:
	}
}

func (c *Client) watch(ctx context.Context, lv *libvirt.Libvirt) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	events, err := lv.LifecycleEvents(ctx)
	if err != nil {
		// Events are a nicety; polling still keeps the menu current.
		log.Printf("lifecycle events unavailable: %v", err)
	}

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		snap := c.snapshot(lv)
		c.publish(snap)
		if snap.Err != nil && !lv.IsConnected() {
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-lv.Disconnected():
			c.publish(Snapshot{Err: fmt.Errorf("lost connection to %s", c.uri)})
			return
		case _, ok := <-events:
			if !ok {
				events = nil // stream ended; fall back to polling
			}
		case <-ticker.C:
		case <-c.refresh:
		}
	}
}

func (c *Client) snapshot(lv *libvirt.Libvirt) Snapshot {
	doms, _, err := lv.ConnectListAllDomains(1,
		libvirt.ConnectListDomainsActive|libvirt.ConnectListDomainsInactive)
	if err != nil {
		return Snapshot{Err: err}
	}
	vms := make([]VM, 0, len(doms))
	for _, d := range doms {
		state, _, err := lv.DomainGetState(d, 0)
		if err != nil {
			continue // domain vanished between list and query
		}
		auto, _ := lv.DomainGetAutostart(d)
		vms = append(vms, VM{Dom: d, State: libvirt.DomainState(state), Autostart: auto == 1})
	}
	sort.Slice(vms, func(i, j int) bool { return vms[i].Dom.Name < vms[j].Dom.Name })
	return Snapshot{VMs: vms}
}

// Do runs fn against the live connection and triggers a refresh afterwards.
func (c *Client) Do(fn func(*libvirt.Libvirt) error) error {
	defer c.Refresh()
	lv, err := c.conn()
	if err != nil {
		return err
	}
	return fn(lv)
}
