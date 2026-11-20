package engine

import (
	"math/bits"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"github.com/Almo7aya/wanscope/internal/sys"
	"github.com/Almo7aya/wanscope/internal/ubus"
	"github.com/Almo7aya/wanscope/internal/uci"
)

// Iface is a logical interface (wan1, relay2, lan...).
type Iface struct {
	Name     string         `json:"name"`
	Dev      string         `json:"dev"`
	Proto    string         `json:"proto"`
	Up       bool           `json:"up"`
	Role     string         `json:"role"` // lan | wan | other
	Kind     string         `json:"kind"` // uplink | tunnel | lan | other
	Prefixes []netip.Prefix `json:"-"`
	Gateway  netip.Addr     `json:"-"`
	MwanID   int            `json:"-"`
	Overhead uint64         `json:"-"`
}

// Topology is a snapshot of what the router's interfaces are for.
type Topology struct {
	Ifaces   []*Iface
	byName   map[string]*Iface
	byDev    map[string]*Iface
	router   map[netip.Addr]string // the router's own addresses -> device
	lan      []netip.Prefix
	gateways map[netip.Addr]string // WAN gateway address -> logical name
	mwanMask uint32
	mwanByID map[int]string
}

type ifaceDump struct {
	Interface []struct {
		Interface string `json:"interface"`
		Up        bool   `json:"up"`
		L3Device  string `json:"l3_device"`
		Device    string `json:"device"`
		Proto     string `json:"proto"`
		IPv4      []struct {
			Address string `json:"address"`
			Mask    int    `json:"mask"`
		} `json:"ipv4-address"`
		IPv6 []struct {
			Address string `json:"address"`
			Mask    int    `json:"mask"`
		} `json:"ipv6-address"`
		Route []struct {
			Target  string `json:"target"`
			Mask    int    `json:"mask"`
			Nexthop string `json:"nexthop"`
		} `json:"route"`
	} `json:"interface"`
}

var tunnelProtos = map[string]bool{
	"wireguard": true, "openvpn": true, "vpnc": true, "l2tp": true, "pptp": true, "gre": true, "gretap": true,
	"ipip": true, "6in4": true, "6to4": true, "6rd": true, "dslite": true, "map": true, "openconnect": true,
	"sstp": true, "xfrm": true, "vxlan": true, "zerotier": true,
}

// LoadTopology builds the snapshot from netifd, the firewall zones and
// mwan3's interface order.
func LoadTopology(bus *ubus.Bus, nl *sys.Netlink, cfg *Config) (*Topology, error) {
	var dump ifaceDump
	if err := bus.Call("network.interface", "dump", nil, &dump); err != nil {
		return nil, err
	}
	t := &Topology{byName: map[string]*Iface{}, byDev: map[string]*Iface{}, router: map[netip.Addr]string{},
		gateways: map[netip.Addr]string{}, mwanByID: map[int]string{}, mwanMask: 0x3f00}

	zones := map[string]string{} // network -> zone role
	if fw, err := uci.Load("firewall"); err == nil {
		for _, z := range fw.ByType("zone") {
			role := "other"
			switch {
			case z.Get("name", "") == "lan":
				role = "lan"
			case z.Bool("masq", false) || z.Get("name", "") == "wan":
				role = "wan"
			}
			for _, n := range z.List("network") {
				if _, ok := zones[n]; !ok || role != "other" {
					zones[n] = role
				}
			}
		}
	}
	lanSet := map[string]bool{}
	for _, l := range cfg.LAN {
		lanSet[l] = true
	}

	for _, d := range dump.Interface {
		if d.Interface == "loopback" {
			continue
		}
		dev := d.L3Device
		if dev == "" {
			dev = d.Device
		}
		ifc := &Iface{Name: d.Interface, Dev: dev, Proto: d.Proto, Up: d.Up}
		hasDefault := false
		for _, a := range d.IPv4 {
			if ip, err := netip.ParseAddr(a.Address); err == nil {
				ifc.Prefixes = append(ifc.Prefixes, netip.PrefixFrom(ip, a.Mask))
			}
		}
		for _, a := range d.IPv6 {
			if ip, err := netip.ParseAddr(a.Address); err == nil {
				ifc.Prefixes = append(ifc.Prefixes, netip.PrefixFrom(ip, a.Mask))
			}
		}
		for _, r := range d.Route {
			if r.Mask == 0 && (r.Target == "0.0.0.0" || r.Target == "::") {
				hasDefault = true
				if gw, err := netip.ParseAddr(r.Nexthop); err == nil && !gw.IsUnspecified() {
					ifc.Gateway = gw
				}
			}
		}
		switch {
		case len(lanSet) > 0 && lanSet[d.Interface]:
			ifc.Role = "lan"
		case len(lanSet) == 0 && zones[d.Interface] == "lan":
			ifc.Role = "lan"
		case zones[d.Interface] == "wan" || hasDefault:
			ifc.Role = "wan"
		default:
			ifc.Role = "other"
		}
		ltype, devtype := 0, ""
		if dev != "" {
			ltype, devtype = sys.LinkType(dev)
			ifc.Overhead = sys.L2Overhead(dev)
		}
		switch ifc.Role {
		case "lan":
			ifc.Kind = "lan"
		case "wan":
			ifc.Kind = "uplink"
			if tunnelProtos[d.Proto] || devtype == "wireguard" || ltype == 65534 ||
				strings.HasPrefix(dev, "tun") || strings.HasPrefix(dev, "wg") || strings.HasPrefix(dev, "tailscale") {
				ifc.Kind = "tunnel"
			}
			if wc := cfg.WANs[d.Interface]; wc != nil && (wc.Kind == "uplink" || wc.Kind == "tunnel") {
				ifc.Kind = wc.Kind
			}
		default:
			ifc.Kind = "other"
		}
		if ifc.Gateway.IsValid() && ifc.Role == "wan" {
			t.gateways[ifc.Gateway] = ifc.Name
		}
		t.Ifaces = append(t.Ifaces, ifc)
		t.byName[ifc.Name] = ifc
		if dev != "" {
			// a device used by several interfaces (wan + wan6) belongs to the first
			if _, ok := t.byDev[dev]; !ok {
				t.byDev[dev] = ifc
			}
		}
	}
	sort.SliceStable(t.Ifaces, func(i, j int) bool { return natLess(t.Ifaces[i].Name, t.Ifaces[j].Name) })

	addrs, err := nl.Addrs()
	if err != nil {
		return nil, err
	}
	for _, a := range addrs {
		t.router[a.Prefix.Addr()] = a.Dev
	}
	for _, ifc := range t.Ifaces {
		if ifc.Role != "lan" {
			continue
		}
		for _, p := range ifc.Prefixes {
			if p.Addr().IsLinkLocalUnicast() {
				continue
			}
			t.lan = append(t.lan, p.Masked())
		}
	}

	if mw, err := uci.Load("mwan3"); err == nil {
		for i, s := range mw.ByType("interface") {
			t.mwanByID[i+1] = s.Name
			if ifc := t.byName[s.Name]; ifc != nil {
				ifc.MwanID = i + 1
			}
		}
		for _, g := range mw.ByType("globals") {
			if m := g.Get("mmx_mask", ""); m != "" {
				if v, err := strconv.ParseUint(strings.TrimPrefix(m, "0x"), 16, 32); err == nil && v != 0 {
					t.mwanMask = uint32(v)
				}
			}
		}
	}
	return t, nil
}

// natLess orders "wan2" before "wan10".
func natLess(a, b string) bool {
	ta, na := splitNum(a)
	tb, nb := splitNum(b)
	if ta != tb {
		return ta < tb
	}
	return na < nb
}

func splitNum(s string) (string, int) {
	i := len(s)
	for i > 0 && s[i-1] >= '0' && s[i-1] <= '9' {
		i--
	}
	n, _ := strconv.Atoi(s[i:])
	return s[:i], n
}

func (t *Topology) Iface(name string) *Iface { return t.byName[name] }

func (t *Topology) IsRouter(a netip.Addr) bool {
	if a.IsLoopback() {
		return true
	}
	_, ok := t.router[a]
	return ok
}

func (t *Topology) IsLAN(a netip.Addr) bool {
	for _, p := range t.lan {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// IfaceOfAddr is the logical interface owning one of the router's addresses.
func (t *Topology) IfaceOfAddr(a netip.Addr) *Iface {
	dev, ok := t.router[a]
	if !ok {
		return nil
	}
	return t.byDev[dev]
}

// WANOfMark maps an mwan3 connection mark to its interface.
func (t *Topology) WANOfMark(mark uint32) string {
	if t.mwanMask == 0 {
		return ""
	}
	id := int((mark & t.mwanMask) >> bits.TrailingZeros32(t.mwanMask))
	return t.mwanByID[id]
}

// Gateway names the WAN whose gateway (modem) has this address.
func (t *Topology) Gateway(a netip.Addr) string { return t.gateways[a] }

func (t *Topology) WANs() []*Iface {
	var out []*Iface
	for _, i := range t.Ifaces {
		if i.Role == "wan" {
			out = append(out, i)
		}
	}
	return out
}

func (t *Topology) LANDevs() []string {
	var out []string
	seen := map[string]bool{}
	for _, i := range t.Ifaces {
		if i.Role == "lan" && i.Dev != "" && !seen[i.Dev] && !strings.HasPrefix(i.Dev, "tailscale") {
			seen[i.Dev] = true
			out = append(out, i.Dev)
		}
	}
	return out
}
