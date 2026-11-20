// Package procs finds which program on the router owns a socket, so the
// router's own traffic can be split by program (VPN clients, DNS server,
// downloads...).
package procs

import (
	"bufio"
	"encoding/hex"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type sockKey struct {
	proto uint8
	local netip.AddrPort
}

type Resolver struct {
	mu       sync.Mutex
	byLocal  map[sockKey]string // exact local address
	byPort   map[sockKey]string // any-address listeners/binds (address unspecified)
	lastScan time.Time
	misses   map[sockKey]time.Time
}

func NewResolver() *Resolver {
	return &Resolver{byLocal: map[sockKey]string{}, byPort: map[sockKey]string{}, misses: map[sockKey]time.Time{}}
}

// Lookup returns the program owning the router's end of a connection, or "".
// Unknown sockets trigger a rescan at most every few seconds.
func (r *Resolver) Lookup(proto uint8, local netip.AddrPort) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p := r.find(proto, local); p != "" {
		return p
	}
	k := sockKey{proto, local}
	if t, ok := r.misses[k]; ok && time.Since(t) < 30*time.Second {
		return ""
	}
	if time.Since(r.lastScan) < 3*time.Second {
		r.misses[k] = time.Now()
		return ""
	}
	r.scan()
	if p := r.find(proto, local); p != "" {
		return p
	}
	r.misses[k] = time.Now()
	return ""
}

func (r *Resolver) find(proto uint8, local netip.AddrPort) string {
	if p, ok := r.byLocal[sockKey{proto, local}]; ok {
		return p
	}
	for _, any := range []netip.Addr{netip.IPv4Unspecified(), netip.IPv6Unspecified()} {
		if p, ok := r.byPort[sockKey{proto, netip.AddrPortFrom(any, local.Port())}]; ok {
			return p
		}
	}
	return ""
}

func (r *Resolver) scan() {
	r.lastScan = time.Now()
	if len(r.misses) > 4096 {
		r.misses = map[sockKey]time.Time{}
	}
	inodes := map[string]sockKey{}
	for _, t := range []struct {
		file  string
		proto uint8
	}{{"/proc/net/tcp", 6}, {"/proc/net/tcp6", 6}, {"/proc/net/udp", 17}, {"/proc/net/udp6", 17}} {
		readSockets(t.file, t.proto, inodes)
	}
	owners := map[string]string{}
	procs, _ := os.ReadDir("/proc")
	for _, p := range procs {
		pid := p.Name()
		if pid[0] < '0' || pid[0] > '9' {
			continue
		}
		fds, err := os.ReadDir("/proc/" + pid + "/fd")
		if err != nil {
			continue
		}
		name := ""
		for _, fd := range fds {
			link, err := os.Readlink("/proc/" + pid + "/fd/" + fd.Name())
			if err != nil || !strings.HasPrefix(link, "socket:[") {
				continue
			}
			ino := link[8 : len(link)-1]
			if _, ok := inodes[ino]; !ok {
				continue
			}
			if name == "" {
				name = comm(pid)
			}
			owners[ino] = name
		}
	}
	byLocal := map[sockKey]string{}
	byPort := map[sockKey]string{}
	for ino, k := range inodes {
		name, ok := owners[ino]
		if !ok {
			continue
		}
		if k.local.Addr().IsUnspecified() {
			byPort[k] = name
		} else {
			byLocal[k] = name
		}
	}
	r.byLocal, r.byPort = byLocal, byPort
	r.misses = map[sockKey]time.Time{}
}

func comm(pid string) string {
	raw, err := os.ReadFile("/proc/" + pid + "/comm")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// readSockets parses /proc/net/{tcp,udp}[6]: local address and inode.
func readSockets(path string, proto uint8, out map[string]sockKey) {
	fh, err := os.Open(path)
	if err != nil {
		return
	}
	defer fh.Close()
	sc := bufio.NewScanner(fh)
	sc.Scan() // header
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 10 || f[9] == "0" {
			continue
		}
		ap, ok := parseHexAddr(f[1])
		if !ok {
			continue
		}
		out[f[9]] = sockKey{proto, ap}
	}
}

// parseHexAddr reads "0100007F:0035" (little-endian 32-bit words).
func parseHexAddr(s string) (netip.AddrPort, bool) {
	h, p, ok := strings.Cut(s, ":")
	if !ok {
		return netip.AddrPort{}, false
	}
	port, err := strconv.ParseUint(p, 16, 16)
	if err != nil {
		return netip.AddrPort{}, false
	}
	raw, err := hex.DecodeString(h)
	if err != nil || (len(raw) != 4 && len(raw) != 16) {
		return netip.AddrPort{}, false
	}
	for i := 0; i+4 <= len(raw); i += 4 {
		raw[i], raw[i+1], raw[i+2], raw[i+3] = raw[i+3], raw[i+2], raw[i+1], raw[i]
	}
	a, _ := netip.AddrFromSlice(raw)
	return netip.AddrPortFrom(a.Unmap(), uint16(port)), true
}
