package sys

import (
	"net/netip"
	"strings"

	"github.com/jsimonetti/rtnetlink/v2"
)

type Addr struct {
	Prefix netip.Prefix // the address with its prefix length
	Dev    string
}

type Neighbor struct {
	IP        netip.Addr
	MAC       string
	Dev       string
	Reachable bool // confirmed lately (not just a stale cache entry)
}

// Netlink reads addresses and neighbours from the kernel.
type Netlink struct{ c *rtnetlink.Conn }

func OpenNetlink() (*Netlink, error) {
	c, err := rtnetlink.Dial(nil)
	if err != nil {
		return nil, err
	}
	return &Netlink{c: c}, nil
}

func (n *Netlink) Close() error { return n.c.Close() }

func (n *Netlink) names() (map[uint32]string, error) {
	links, err := n.c.Link.List()
	if err != nil {
		return nil, err
	}
	m := make(map[uint32]string, len(links))
	for _, l := range links {
		if l.Attributes != nil {
			m[l.Index] = l.Attributes.Name
		}
	}
	return m, nil
}

func (n *Netlink) Addrs() ([]Addr, error) {
	names, err := n.names()
	if err != nil {
		return nil, err
	}
	msgs, err := n.c.Address.List()
	if err != nil {
		return nil, err
	}
	out := make([]Addr, 0, len(msgs))
	for _, m := range msgs {
		if m.Attributes == nil {
			continue
		}
		ip := m.Attributes.Local
		if ip == nil {
			ip = m.Attributes.Address
		}
		a, ok := netip.AddrFromSlice(ip)
		if !ok {
			continue
		}
		a = a.Unmap()
		out = append(out, Addr{Prefix: netip.PrefixFrom(a, int(m.PrefixLength)), Dev: names[m.Index]})
	}
	return out, nil
}

const (
	nudReachable  = 0x02
	nudDelay      = 0x08
	nudProbe      = 0x10
	nudPermanent  = 0x80
	nudIncomplete = 0x01
	nudFailed     = 0x20
	nudNoARP      = 0x40
)

func (n *Netlink) Neighbors() ([]Neighbor, error) {
	names, err := n.names()
	if err != nil {
		return nil, err
	}
	msgs, err := n.c.Neigh.List()
	if err != nil {
		return nil, err
	}
	out := make([]Neighbor, 0, len(msgs))
	for _, m := range msgs {
		if m.Attributes == nil || len(m.Attributes.LLAddress) != 6 || m.State&(nudIncomplete|nudFailed|nudNoARP) != 0 {
			continue
		}
		ip, ok := netip.AddrFromSlice(m.Attributes.Address)
		if !ok {
			continue
		}
		mac := strings.ToLower(m.Attributes.LLAddress.String())
		if mac == "00:00:00:00:00:00" {
			continue
		}
		out = append(out, Neighbor{IP: ip.Unmap(), MAC: mac, Dev: names[m.Index], Reachable: m.State&(nudReachable|nudDelay|nudProbe|nudPermanent) != 0})
	}
	return out, nil
}
