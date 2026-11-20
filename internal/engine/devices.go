package engine

import (
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Almo7aya/wanscope/internal/sys"
)

const RouterKey = "router"

// Device is a client of the router, known by its MAC address (or by its
// IP when the router can't see the MAC, as for VPN peers).
type Device struct {
	Key       string           `json:"key"`
	MAC       string           `json:"mac,omitempty"`
	Hostname  string           `json:"hostname,omitempty"` // from DHCP
	IPs       map[string]int64 `json:"ips"`                // address -> last seen
	FirstSeen int64            `json:"first_seen"`
	LastSeen  int64            `json:"last_seen"` // last traffic
	Approved  bool             `json:"approved"`
	Found     string           `json:"found,omitempty"` // name the device gave on the LAN (NetBIOS, mDNS)
	FoundAt   int64            `json:"found_at,omitempty"`
	Reachable int64            `json:"-"` // last time the neighbour table had it
}

// Registry maps addresses to devices and remembers devices across restarts.
type Registry struct {
	mu      sync.RWMutex
	path    string
	devs    map[string]*Device
	byIP    map[netip.Addr]string
	static  map[string]string // MAC -> name from dhcp/ethers
	dirty   bool
	saved   time.Time
	firstOn bool // no registry file before: everything seen now is not new
}

func NewRegistry(dir string) *Registry {
	r := &Registry{path: filepath.Join(dir, "devices.json"), devs: map[string]*Device{}, byIP: map[netip.Addr]string{}}
	raw, err := os.ReadFile(r.path)
	if err != nil {
		r.firstOn = true
		return r
	}
	var list []*Device
	if json.Unmarshal(raw, &list) == nil {
		for _, d := range list {
			if d.IPs == nil {
				d.IPs = map[string]int64{}
			}
			r.devs[d.Key] = d
			for ip := range d.IPs {
				if a, err := netip.ParseAddr(ip); err == nil {
					r.byIP[a] = d.Key
				}
			}
		}
	}
	return r
}

// Refresh takes the neighbour table and DHCP leases.
func (r *Registry) Refresh(neigh []sys.Neighbor, leases []sys.Lease, static map[string]string, topo *Topology, now int64) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.static = static
	var fresh []string
	see := func(mac string, ip netip.Addr, host string, reachable bool) {
		if topo != nil && topo.IsRouter(ip) {
			return
		}
		d := r.devs[mac]
		if d == nil && ip.IsValid() && !ip.Is4() && (ip.IsLinkLocalUnicast() || topo == nil || !topo.IsLAN(ip)) {
			// a MAC known only by its IPv6 link-local address (phones rotating
			// private addresses, IPv6 chatter on an IPv4 network): not a device
			return
		}
		if d == nil {
			d = &Device{Key: mac, MAC: mac, IPs: map[string]int64{}, FirstSeen: now, Approved: r.firstOn}
			r.devs[mac] = d
			fresh = append(fresh, mac)
		}
		if host != "" && d.Hostname != host {
			d.Hostname = host
		}
		if ip.IsValid() {
			if _, ok := d.IPs[ip.String()]; !ok {
				r.dirty = true
			}
			d.IPs[ip.String()] = now
			if old, ok := r.byIP[ip]; ok && old != mac {
				// the address moved to another device
				if od := r.devs[old]; od != nil {
					delete(od.IPs, ip.String())
				}
			}
			r.byIP[ip] = mac
		}
		if reachable {
			d.Reachable = now
		}
		r.dirty = true
	}
	for _, l := range leases {
		see(l.MAC, l.IP, l.Name, false)
	}
	for _, n := range neigh {
		if topo != nil && n.Dev != "" {
			if ifc := topo.byDev[n.Dev]; ifc != nil && ifc.Role != "lan" {
				continue // modems and peers on WAN links
			}
		}
		see(n.MAC, n.IP, "", n.Reachable)
	}
	// forget devices that never moved a byte and have no IPv4 address (left
	// over from IPv6 neighbour chatter), and addresses unused for a week
	for k, d := range r.devs {
		if d.LastSeen == 0 && d.Hostname == "" && !hasV4(d) && now-d.FirstSeen > 600 && r.static[k] == "" {
			delete(r.devs, k)
			continue
		}
		for ip, t := range d.IPs {
			if now-t > 7*86400 {
				delete(d.IPs, ip)
				if a, err := netip.ParseAddr(ip); err == nil && r.byIP[a] == d.Key {
					delete(r.byIP, a)
				}
			}
		}
	}
	r.firstOn = false
	return fresh
}

// KeyOf is the device key for a LAN address.
func (r *Registry) KeyOf(ip netip.Addr) string {
	r.mu.RLock()
	k, ok := r.byIP[ip]
	r.mu.RUnlock()
	if ok {
		return k
	}
	return "ip:" + ip.String()
}

// Seen marks traffic from a device, creating IP-keyed devices on first use.
func (r *Registry) Seen(key string, ip netip.Addr, now int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.devs[key]
	if d == nil {
		d = &Device{Key: key, IPs: map[string]int64{}, FirstSeen: now, Approved: true}
		r.devs[key] = d
		r.dirty = true
	}
	d.LastSeen = now
	if ip.IsValid() && key != RouterKey {
		d.IPs[ip.String()] = now
	}
}

func (r *Registry) Get(key string) *Device {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if d := r.devs[key]; d != nil {
		c := *d
		c.IPs = make(map[string]int64, len(d.IPs))
		for k, v := range d.IPs {
			c.IPs[k] = v
		}
		return &c
	}
	return nil
}

func (r *Registry) All() []*Device {
	r.mu.RLock()
	keys := make([]string, 0, len(r.devs))
	for k := range r.devs {
		keys = append(keys, k)
	}
	r.mu.RUnlock()
	sort.Strings(keys)
	out := make([]*Device, 0, len(keys))
	for _, k := range keys {
		if d := r.Get(k); d != nil {
			out = append(out, d)
		}
	}
	return out
}

func (r *Registry) StaticName(mac string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.static[mac]
}

// IPsOf lists a device's current addresses (seen in the last day).
func (r *Registry) IPsOf(key string, now int64) []netip.Addr {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d := r.devs[key]
	if d == nil {
		return nil
	}
	var out []netip.Addr
	for ip, t := range d.IPs {
		if now-t < 86400 {
			if a, err := netip.ParseAddr(ip); err == nil && !a.IsLinkLocalUnicast() {
				out = append(out, a)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Less(out[j]) })
	return out
}

// SetFound records a name the device answered with on the LAN.
func (r *Registry) SetFound(key, name string, now int64) {
	r.mu.Lock()
	if d := r.devs[key]; d != nil {
		d.Found, d.FoundAt = name, now
		r.dirty = true
	}
	r.mu.Unlock()
}

func (r *Registry) Approve(key string, ok bool) {
	r.mu.Lock()
	if d := r.devs[key]; d != nil {
		d.Approved = ok
		r.dirty = true
	}
	r.mu.Unlock()
}

func (r *Registry) Forget(key string) {
	r.mu.Lock()
	if d := r.devs[key]; d != nil {
		for ip := range d.IPs {
			if a, err := netip.ParseAddr(ip); err == nil && r.byIP[a] == key {
				delete(r.byIP, a)
			}
		}
		delete(r.devs, key)
		r.dirty = true
	}
	r.mu.Unlock()
}

// Save writes the registry if it changed (at most every few minutes unless
// forced).
func (r *Registry) Save(force bool) error {
	r.mu.Lock()
	if !r.dirty || (!force && time.Since(r.saved) < 5*time.Minute) {
		r.mu.Unlock()
		return nil
	}
	list := make([]*Device, 0, len(r.devs))
	for _, d := range r.devs {
		list = append(list, d)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Key < list[j].Key })
	raw, err := json.Marshal(list)
	r.dirty = false
	r.saved = time.Now()
	r.mu.Unlock()
	if err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}

func hasV4(d *Device) bool {
	for ip := range d.IPs {
		if a, err := netip.ParseAddr(ip); err == nil && a.Is4() {
			return true
		}
	}
	return false
}

// IsMAC tells MAC keys from "ip:" and "router" keys.
func IsMAC(key string) bool { return len(key) == 17 && strings.Count(key, ":") == 5 }

// RandomMAC reports a locally administered (often randomised, "private")
// address.
func RandomMAC(mac string) bool {
	if !IsMAC(mac) {
		return false
	}
	b := mac[1]
	return strings.ContainsRune("2367abef", rune(b))
}
