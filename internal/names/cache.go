// Package names works out which host name a connection went to: from the
// server name in its TLS or QUIC ClientHello, else from the DNS answer that
// gave the client that address.
package names

import (
	"bufio"
	"encoding/json"
	"io"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

type dnsKey struct{ client, ip netip.Addr }

type dnsEntry struct {
	name, cname string
	exp         int64
}

type helloKey struct {
	proto    uint8
	src, dst netip.AddrPort
}

type helloEntry struct {
	name string
	ech  bool
	exp  int64
}

type Stats struct {
	DNS, TLS, QUIC, Packets uint64
}

type Cache struct {
	mu    sync.RWMutex
	dns   map[dnsKey]dnsEntry
	any   map[netip.Addr]dnsEntry
	hello map[helloKey]helloEntry
	quic  *quicAssembler
	last  int64

	nDNS, nTLS, nQUIC atomic.Uint64
}

func NewCache() *Cache {
	return &Cache{
		dns:   map[dnsKey]dnsEntry{},
		any:   map[netip.Addr]dnsEntry{},
		hello: map[helloKey]helloEntry{},
		quic:  newQuicAssembler(),
	}
}

const (
	dnsKeep   = 3 * 3600 // answers stay usable well past their TTL: connections outlive it
	helloKeep = 120
	maxDNS    = 200000
)

// Handle takes one sniffed packet.
func (c *Cache) Handle(p *Packet) {
	now := time.Now()
	switch {
	case p.Proto == 17 && p.Src.Port() == 53:
		ans := ParseDNS(p.Payload)
		if len(ans) == 0 {
			return
		}
		c.nDNS.Add(1)
		client := p.Dst.Addr()
		c.mu.Lock()
		for _, a := range ans {
			e := dnsEntry{name: a.Name, cname: a.CNAME, exp: now.Unix() + int64(a.TTL) + dnsKeep}
			c.dns[dnsKey{client, a.IP.Unmap()}] = e
			c.any[a.IP.Unmap()] = e
		}
		c.mu.Unlock()
	case p.Proto == 6 && p.Dst.Port() == 443:
		name, ech := TLSRecordHello(p.Payload)
		if name == "" {
			return
		}
		c.nTLS.Add(1)
		c.putHello(6, p, name, ech, now)
	case p.Proto == 17 && p.Dst.Port() == 443:
		if name, srcs := c.quic.add(p.Src, p.Payload, now); name != "" {
			c.nQUIC.Add(1)
			for _, s := range srcs {
				c.putHello(17, &Packet{Src: s, Dst: p.Dst}, name, false, now)
			}
		}
	}
	c.expire(now)
}

func (c *Cache) putHello(proto uint8, p *Packet, name string, ech bool, now time.Time) {
	k := helloKey{proto, netip.AddrPortFrom(p.Src.Addr().Unmap(), p.Src.Port()), netip.AddrPortFrom(p.Dst.Addr().Unmap(), p.Dst.Port())}
	c.mu.Lock()
	c.hello[k] = helloEntry{name: name, ech: ech, exp: now.Unix() + helloKeep}
	c.mu.Unlock()
}

func (c *Cache) expire(now time.Time) {
	t := now.Unix()
	c.mu.RLock()
	due := t-c.last >= 60 || len(c.dns) > maxDNS
	c.mu.RUnlock()
	if !due {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.last = t
	for k, e := range c.dns {
		if e.exp < t || len(c.dns) > maxDNS {
			delete(c.dns, k)
		}
	}
	for k, e := range c.any {
		if e.exp < t || len(c.any) > maxDNS {
			delete(c.any, k)
		}
	}
	for k, e := range c.hello {
		if e.exp < t {
			delete(c.hello, k)
		}
	}
}

// Name is what is known about a connection's server: the host the client
// named, an alias that helps classify it (a CNAME), and where it came from
// ("sni", "quic", "dns" for this client's own lookup, "dns*" for another
// client's).
type Name struct {
	Host, Alias, Source string
}

func (c *Cache) Lookup(proto uint8, client, server netip.AddrPort) Name {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out Name
	if h, ok := c.hello[helloKey{proto, client, server}]; ok {
		out = Name{Host: h.name, Source: "sni"}
		if proto == 17 {
			out.Source = "quic"
		}
		if !h.ech {
			if d, ok := c.dns[dnsKey{client.Addr(), server.Addr()}]; ok {
				out.Alias = d.cname
			}
			return out
		}
		// an encrypted hello names only the provider: prefer the lookup
	}
	if d, ok := c.dns[dnsKey{client.Addr(), server.Addr()}]; ok {
		return Name{Host: d.name, Alias: d.cname, Source: "dns"}
	}
	if d, ok := c.any[server.Addr()]; ok {
		return Name{Host: d.name, Alias: d.cname, Source: "dns*"}
	}
	return out
}

func (c *Cache) Stats() Stats {
	return Stats{DNS: c.nDNS.Load(), TLS: c.nTLS.Load(), QUIC: c.nQUIC.Load()}
}

// SeedAdGuard reads the end of an AdGuard Home query log (JSON lines that
// carry each answer in wire format), so connections opened before wanscope
// started still get names. It returns how many answers it took.
func (c *Cache) SeedAdGuard(path string, maxBytes int64) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > maxBytes {
		f.Seek(st.Size()-maxBytes, io.SeekStart)
	}
	now := time.Now().Unix()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	n := 0
	c.mu.Lock()
	defer c.mu.Unlock()
	for sc.Scan() {
		var e struct {
			T      time.Time `json:"T"`
			IP     string    `json:"IP"`
			Answer []byte    `json:"Answer"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil || len(e.Answer) == 0 {
			continue
		}
		client, err := netip.ParseAddr(e.IP)
		if err != nil {
			continue
		}
		for _, a := range ParseDNS(e.Answer) {
			exp := e.T.Unix() + int64(a.TTL) + dnsKeep
			if exp < now {
				continue
			}
			d := dnsEntry{name: a.Name, cname: a.CNAME, exp: exp}
			c.dns[dnsKey{client.Unmap(), a.IP.Unmap()}] = d
			c.any[a.IP.Unmap()] = d
			n++
		}
	}
	return n
}
