package engine

import (
	"encoding/binary"
	"math/rand/v2"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// Devices that didn't give DHCP a name are asked on the LAN: Windows answers
// a NetBIOS name query, Apple and most Android devices a unicast mDNS
// reverse lookup. Nothing leaves the LAN.

var (
	askMu   sync.Mutex
	askLast = map[string]int64{}
)

func (e *Engine) discoverNames(now int64) {
	cfg := e.Config()
	var jobs []func()
	for _, d := range e.Devs.All() {
		if !IsMAC(d.Key) || d.Hostname != "" || e.Devs.StaticName(d.Key) != "" {
			continue
		}
		if dc := cfg.Devices[d.Key]; dc != nil && dc.Name != "" {
			continue
		}
		if d.Found != "" && now-d.FoundAt < 86400 {
			continue
		}
		askMu.Lock()
		due := now-askLast[d.Key] >= 1800
		if due {
			askLast[d.Key] = now
		}
		askMu.Unlock()
		if !due {
			continue
		}
		var ip netip.Addr
		for _, a := range e.Devs.IPsOf(d.Key, now) {
			if a.Is4() && !e.Topo().IsRouter(a) {
				ip = a
				break
			}
		}
		if !ip.IsValid() || e.Topo().Gateway(ip) != "" {
			continue
		}
		key := d.Key
		jobs = append(jobs, func() {
			name := nbnsName(ip)
			if name == "" {
				name = mdnsName(ip)
			}
			if name != "" {
				e.Devs.SetFound(key, name, time.Now().Unix())
			}
		})
	}
	// a few at a time
	sem := make(chan struct{}, 4)
	for _, j := range jobs {
		sem <- struct{}{}
		go func(j func()) { defer func() { <-sem }(); j() }(j)
	}
}

func exchange(ip netip.Addr, port uint16, q []byte) []byte {
	c, err := net.DialUDP("udp4", nil, net.UDPAddrFromAddrPort(netip.AddrPortFrom(ip, port)))
	if err != nil {
		return nil
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(1500 * time.Millisecond))
	if _, err := c.Write(q); err != nil {
		return nil
	}
	buf := make([]byte, 1500)
	n, err := c.Read(buf)
	if err != nil {
		return nil
	}
	return buf[:n]
}

// nbnsName asks for a host's NetBIOS names (node status) and returns its
// workstation name.
func nbnsName(ip netip.Addr) string {
	q := make([]byte, 0, 50)
	id := uint16(rand.Uint32())
	q = binary.BigEndian.AppendUint16(q, id)
	q = append(q, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0)
	// "*" padded with NULs, first-level encoded
	q = append(q, 0x20, 'C', 'K')
	for i := 0; i < 30; i++ {
		q = append(q, 'A')
	}
	q = append(q, 0, 0, 0x21, 0, 1)
	r := exchange(ip, 137, q)
	if len(r) < 57 || binary.BigEndian.Uint16(r) != id {
		return ""
	}
	p := 12 + 34 + 2 + 2 + 4 + 2 // header, name, type, class, ttl, rdlength
	if p >= len(r) {
		return ""
	}
	n := int(r[p])
	p++
	for i := 0; i < n && p+18 <= len(r); i, p = i+1, p+18 {
		name := strings.TrimRight(string(r[p:p+15]), " \x00")
		suffix, flags := r[p+15], binary.BigEndian.Uint16(r[p+16:])
		if suffix == 0 && flags&0x8000 == 0 && name != "" && printable(name) {
			return name
		}
	}
	return ""
}

// mdnsName sends a unicast ("legacy") mDNS reverse lookup to the host.
func mdnsName(ip netip.Addr) string {
	a := ip.As4()
	rev := []string{itoa(a[3]), itoa(a[2]), itoa(a[1]), itoa(a[0]), "in-addr", "arpa"}
	id := uint16(rand.Uint32())
	q := binary.BigEndian.AppendUint16(nil, id)
	q = append(q, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0)
	for _, l := range rev {
		q = append(q, byte(len(l)))
		q = append(q, l...)
	}
	q = append(q, 0, 0, 12, 0x80, 1) // PTR, IN with unicast-response
	r := exchange(ip, 5353, q)
	if len(r) < 12 || binary.BigEndian.Uint16(r[6:]) == 0 {
		return ""
	}
	// skip questions, read the first PTR answer
	off := 12
	for i := 0; i < int(binary.BigEndian.Uint16(r[4:])); i++ {
		_, n, ok := dnsReadName(r, off)
		if !ok {
			return ""
		}
		off = n + 4
	}
	_, n, ok := dnsReadName(r, off)
	if !ok || n+10 > len(r) || binary.BigEndian.Uint16(r[n:]) != 12 {
		return ""
	}
	name, _, ok := dnsReadName(r, n+10)
	if !ok {
		return ""
	}
	name = strings.TrimSuffix(strings.TrimSuffix(name, "."), ".local")
	if !printable(name) {
		return ""
	}
	return name
}

func itoa(b byte) string {
	if b < 10 {
		return string('0' + b)
	}
	return strings.TrimLeft(string([]byte{'0' + b/100, '0' + b/10%10, '0' + b%10}), "0")
}

func printable(s string) bool {
	for _, r := range s {
		if r < 32 || r == 127 {
			return false
		}
	}
	return len(s) > 0 && len(s) < 64
}

// dnsReadName reads a (compressed) DNS name; it keeps the case.
func dnsReadName(b []byte, off int) (string, int, bool) {
	var parts []string
	end, jumps := -1, 0
	for {
		if off >= len(b) {
			return "", 0, false
		}
		l := int(b[off])
		switch {
		case l == 0:
			if end < 0 {
				end = off + 1
			}
			return strings.Join(parts, "."), end, true
		case l&0xc0 == 0xc0:
			if off+1 >= len(b) || jumps > 16 {
				return "", 0, false
			}
			if end < 0 {
				end = off + 2
			}
			off = int(binary.BigEndian.Uint16(b[off:]) & 0x3fff)
			jumps++
		default:
			if off+1+l > len(b) {
				return "", 0, false
			}
			parts = append(parts, string(b[off+1:off+1+l]))
			off += 1 + l
		}
	}
}
