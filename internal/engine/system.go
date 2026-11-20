package engine

import (
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Almo7aya/wanscope/internal/uci"
)

func readSysctl(name string) string {
	raw, err := os.ReadFile("/proc/sys/" + name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func writeSysctl(name, v string) error {
	return os.WriteFile("/proc/sys/"+name, []byte(v), 0o644)
}

// offloadOn reports firewall flow offloading (software or hardware).
func offloadOn() bool {
	fw, err := uci.Load("firewall")
	if err != nil {
		return false
	}
	for _, d := range fw.ByType("defaults") {
		if d.Bool("flow_offloading", false) || d.Bool("flow_offloading_hw", false) {
			return true
		}
	}
	return false
}

func parseAddr(s string) (netip.Addr, error) { return netip.ParseAddr(s) }

// refreshTunnels learns which connections carry WireGuard tunnels: those to
// a peer's endpoint, and those of a program relaying a peer on loopback
// (engarde, udp2raw...).
func (e *Engine) refreshTunnels() {
	out, err := exec.Command("wg", "show", "all", "endpoints").Output()
	if err != nil {
		return
	}
	ends := map[netip.AddrPort]string{}
	procs := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		ep, err := netip.ParseAddrPort(f[2])
		if err != nil {
			continue
		}
		ep = netip.AddrPortFrom(ep.Addr().Unmap(), ep.Port())
		if ep.Addr().IsLoopback() {
			if p := e.procs.Lookup(17, ep); p != "" {
				procs[p] = f[0]
			}
			continue
		}
		ends[ep] = f[0]
	}
	e.mu.Lock()
	e.tunEnds, e.tunProcs = ends, procs
	e.mu.Unlock()
}

// adguardLog finds a running AdGuard Home's query log.
func adguardLog() string {
	procs, _ := os.ReadDir("/proc")
	for _, p := range procs {
		comm, err := os.ReadFile("/proc/" + p.Name() + "/comm")
		if err != nil || strings.TrimSpace(string(comm)) != "AdGuardHome" {
			continue
		}
		raw, _ := os.ReadFile("/proc/" + p.Name() + "/cmdline")
		args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
		work, conf := "", ""
		for i := 0; i+1 < len(args); i++ {
			switch args[i] {
			case "-w", "--work-dir":
				work = args[i+1]
			case "-c", "--config":
				conf = args[i+1]
			}
		}
		if work == "" && len(args) > 0 {
			work = filepath.Dir(args[0])
		}
		if conf != "" {
			// querylog.dir_path, when set, moves the log
			if y, err := os.ReadFile(conf); err == nil {
				in := false
				for _, l := range strings.Split(string(y), "\n") {
					if strings.HasPrefix(l, "querylog:") {
						in = true
						continue
					}
					if in && len(l) > 0 && l[0] != ' ' {
						break
					}
					if v, ok := strings.CutPrefix(strings.TrimSpace(l), "dir_path:"); ok && in {
						if v = strings.Trim(strings.TrimSpace(v), `"'`); v != "" {
							return filepath.Join(v, "querylog.json")
						}
					}
				}
			}
		}
		return filepath.Join(work, "data", "querylog.json")
	}
	return ""
}

// guestSince starts guest control: when it was just turned on (from the
// settings page), every device known now counts as approved.
func (e *Engine) guestSince(cfg *Config) {
	if cfg.Guest.Policy == "allow" || cfg.Guest.Policy == "" {
		if cfg.Guest.Since > 0 {
			// off again: the next time it is turned on starts afresh
			cfg.Guest.Since = 0
			uci.Batch(ConfigName, []string{"delete " + ConfigName + ".guest.since"})
		}
		return
	}
	if cfg.Guest.Since > 0 {
		return
	}
	now := time.Now().Unix()
	cfg.Guest.Since = now
	for _, d := range e.Devs.All() {
		e.Devs.Approve(d.Key, true)
	}
	uci.Batch(ConfigName, []string{"set " + ConfigName + ".guest=guest", "set " + ConfigName + ".guest.since=" + uci.Quote(strconv.FormatInt(now, 10))})
}
