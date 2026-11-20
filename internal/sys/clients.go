package sys

import (
	"bufio"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // OpenWrt keeps a POSIX TZ string; zone names need the database

	"github.com/Almo7aya/wanscope/internal/uci"
)

type Lease struct {
	MAC     string
	IP      netip.Addr
	Name    string
	Expires int64
}

// ReadLeases parses a dnsmasq lease file ("expiry mac ip name clientid").
func ReadLeases(path string) []Lease {
	fh, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer fh.Close()
	var out []Lease
	sc := bufio.NewScanner(fh)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 {
			continue
		}
		ip, err := netip.ParseAddr(f[2])
		if err != nil {
			continue
		}
		exp, _ := strconv.ParseInt(f[0], 10, 64)
		name := f[3]
		if name == "*" {
			name = ""
		}
		out = append(out, Lease{MAC: strings.ToLower(f[1]), IP: ip, Name: name, Expires: exp})
	}
	return out
}

// StaticNames returns names given to MACs in /etc/config/dhcp host sections
// and /etc/ethers.
func StaticNames() map[string]string {
	out := map[string]string{}
	if fh, err := os.Open("/etc/ethers"); err == nil {
		sc := bufio.NewScanner(fh)
		for sc.Scan() {
			f := strings.Fields(sc.Text())
			if len(f) >= 2 && !strings.HasPrefix(f[0], "#") {
				if _, err := netip.ParseAddr(f[1]); err != nil {
					out[strings.ToLower(f[0])] = f[1]
				}
			}
		}
		fh.Close()
	}
	if f, err := uci.Load("dhcp"); err == nil {
		for _, s := range f.ByType("host") {
			name := s.Get("name", "")
			if name == "" {
				continue
			}
			for _, mac := range s.List("mac") {
				out[strings.ToLower(mac)] = name
			}
		}
	}
	return out
}

// Hostname is the router's name from /etc/config/system.
func Hostname() string {
	if f, err := uci.Load("system"); err == nil {
		for _, s := range f.ByType("system") {
			if h := s.Get("hostname", ""); h != "" {
				return h
			}
		}
	}
	h, _ := os.Hostname()
	return h
}

// Location is the router's time zone: the zone name from /etc/config/system,
// else a fixed offset parsed from /etc/TZ ("<+03>-3", "UTC").
func Location() *time.Location {
	if f, err := uci.Load("system"); err == nil {
		for _, s := range f.ByType("system") {
			if z := s.Get("zonename", ""); z != "" {
				if loc, err := time.LoadLocation(strings.ReplaceAll(z, " ", "_")); err == nil {
					return loc
				}
			}
		}
	}
	if raw, err := os.ReadFile("/etc/TZ"); err == nil {
		if loc := posixFixed(strings.TrimSpace(string(raw))); loc != nil {
			return loc
		}
	}
	return time.UTC
}

// posixFixed understands the standard part of a POSIX TZ string; DST rules
// are ignored (zone names cover those).
func posixFixed(tz string) *time.Location {
	name := tz
	i := 0
	if strings.HasPrefix(tz, "<") {
		j := strings.IndexByte(tz, '>')
		if j < 0 {
			return nil
		}
		name, i = tz[1:j], j+1
	} else {
		for i < len(tz) && (tz[i] < '0' || tz[i] > '9') && tz[i] != '+' && tz[i] != '-' {
			i++
		}
		name = tz[:i]
	}
	j := i
	for j < len(tz) && strings.IndexByte("+-0123456789:", tz[j]) >= 0 {
		j++
	}
	off := tz[i:j]
	if off == "" {
		return time.FixedZone(name, 0)
	}
	sign := 1
	if off[0] == '-' {
		sign, off = -1, off[1:]
	} else if off[0] == '+' {
		off = off[1:]
	}
	parts := strings.Split(off, ":")
	secs := 0
	for k, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}
		secs += n * []int{3600, 60, 1}[min(k, 2)]
	}
	// POSIX offsets are west of UTC
	return time.FixedZone(name, -sign*secs)
}
