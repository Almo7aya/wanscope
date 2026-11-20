// Package sys reads what the router knows about its interfaces, addresses,
// neighbours and clients.
package sys

import (
	"bufio"
	"bytes"
	"os"
	"strconv"
	"strings"
)

// LinkStats are a Linux device's counters.
type LinkStats struct {
	RxBytes, RxPackets, TxBytes, TxPackets uint64
}

// ReadLinkStats parses /proc/net/dev: one read for every device.
func ReadLinkStats() (map[string]LinkStats, error) {
	raw, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return nil, err
	}
	out := make(map[string]LinkStats, 32)
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		line := sc.Text()
		i := strings.IndexByte(line, ':')
		if i < 0 {
			continue
		}
		name := strings.TrimSpace(line[:i])
		f := strings.Fields(line[i+1:])
		if len(f) < 10 {
			continue
		}
		n := func(k int) uint64 { v, _ := strconv.ParseUint(f[k], 10, 64); return v }
		out[name] = LinkStats{RxBytes: n(0), RxPackets: n(1), TxBytes: n(8), TxPackets: n(9)}
	}
	return out, nil
}

// LinkType returns the ARPHRD type of a device (1 = Ethernet, 512 = PPP,
// 65534 = none, as for WireGuard and tun) and its DEVTYPE, if any.
func LinkType(dev string) (int, string) {
	t := 0
	if raw, err := os.ReadFile("/sys/class/net/" + dev + "/type"); err == nil {
		t, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
	}
	devtype := ""
	if raw, err := os.ReadFile("/sys/class/net/" + dev + "/uevent"); err == nil {
		for _, l := range strings.Split(string(raw), "\n") {
			if v, ok := strings.CutPrefix(l, "DEVTYPE="); ok {
				devtype = v
			}
		}
	}
	return t, devtype
}

// L2Overhead is the per-packet link header that /proc/net/dev counts on top
// of the IP packet: 14 bytes on Ethernet-like devices, nothing on tunnels.
// Subtracting it gives IP bytes, which is what conntrack and carriers count.
func L2Overhead(dev string) uint64 {
	t, _ := LinkType(dev)
	if t == 1 {
		return 14
	}
	return 0
}
