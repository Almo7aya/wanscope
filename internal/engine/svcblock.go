package engine

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/netip"
	"os/exec"
	"sort"
	"strings"
	"sync"

	"github.com/Almo7aya/wanscope/internal/names"
	"github.com/Almo7aya/wanscope/internal/ubus"
	"github.com/Almo7aya/wanscope/internal/uci"
)

// Blocking a service blocks its IPv4 addresses: an nftables set per blocked service (in wanscope's table) holds
// the addresses its connections use, and traffic to them is dropped, from one device or from every device.
// Nothing is filtered in DNS. The addresses are learnt from the service's live connections and from DNS answers
// that name it (so a new server is blocked from its first packet), and stay blocked until svcKeep after they were
// last seen. An address a blocked service shares with others (a big CDN's) is blocked for them too.

const (
	svcKeep    = 6 * 3600 // seconds an address stays blocked after it was last seen
	svcMaxAddr = 8192     // addresses kept per service
)

type dnsAnswer struct {
	client, ip  netip.Addr
	name, cname string
}

type svcBlocks struct {
	mu      sync.Mutex
	seen    map[string]map[netip.Addr]int64 // set name -> address -> last seen
	inTable map[string]map[netip.Addr]bool  // what the table's sets hold now
	pending map[string][]netip.Addr         // to add at the next flush
	answers chan dnsAnswer
	pruned  int64
}

func newSvcBlocks() *svcBlocks {
	return &svcBlocks{seen: map[string]map[netip.Addr]int64{}, inTable: map[string]map[netip.Addr]bool{},
		pending: map[string][]netip.Addr{}, answers: make(chan dnsAnswer, 4096)}
}

// svcSet is a blocked service's set name in the table (service names can hold anything; set names can't).
func svcSet(svc string) string {
	h := fnv.New32a()
	h.Write([]byte(svc))
	return fmt.Sprintf("svc_%08x", h.Sum32())
}

// dnsAnswer is the name cache's OnAnswer: queued for the next tick, dropped when the queue is full.
func (b *svcBlocks) dnsAnswer(client, ip netip.Addr, name, cname string) {
	if !ip.Is4() {
		return
	}
	select {
	case b.answers <- dnsAnswer{client, ip, name, cname}:
	default:
	}
}

// learn notes that svc uses ip (now).
func (b *svcBlocks) learn(svc string, ip netip.Addr, now int64) {
	ip = ip.Unmap()
	if !ip.Is4() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
		return
	}
	set := svcSet(svc)
	b.mu.Lock()
	defer b.mu.Unlock()
	m := b.seen[set]
	if m == nil {
		m = map[netip.Addr]int64{}
		b.seen[set] = m
	}
	if _, ok := m[ip]; !ok {
		if len(m) >= svcMaxAddr {
			return
		}
		b.pending[set] = append(b.pending[set], ip)
	}
	m[ip] = now
}

// learnBlocked finds blocked services' addresses in the live connections and the DNS answers that came in. The
// caller holds e.mu.
func (e *Engine) learnBlocked(cfg *Config, rules *names.Rules, now int64) {
	if !cfg.AnyServiceBlocked() {
		for {
			select {
			case <-e.blocks.answers:
			default:
				return
			}
		}
	}
	for _, fs := range e.flows {
		if fs.Dev != "" && !fs.LocalOnly && fs.Tunnel == "" && cfg.ServiceBlocked(fs.Dev, fs.Service) {
			e.blocks.learn(fs.Service, fs.Remote.Addr(), now)
		}
	}
	for {
		select {
		case a := <-e.blocks.answers:
			// what the connection would be called: the service a rule names, else the site
			svc := names.Site(a.name)
			if s := rules.Classify(a.name, a.cname, a.ip, 6, 443); s != nil {
				svc = s.Name
			}
			if dev := e.Devs.KeyOf(a.client); cfg.ServiceBlocked(dev, svc) {
				e.blocks.learn(svc, a.ip, now)
			}
		default:
			return
		}
	}
}

// blockRules are the table's sets and rules for the blocked services: "set" lines and "rule" lines.
func (p *Policy) blockRules(cfg *Config, now int64) (sets, rules []string) {
	byDev := map[string][]string{}
	for k, d := range cfg.Devices {
		if len(d.BlockServices) > 0 {
			byDev[k] = d.BlockServices
		}
	}
	all := map[string]bool{}
	for _, s := range cfg.BlockServices {
		all[s] = true
	}
	setOf := map[string]bool{}
	for s := range all {
		setOf[svcSet(s)] = true
		rules = append(rules, fmt.Sprintf("ip daddr @%s counter drop comment %q", svcSet(s), "svc|"+svcSet(s)+"|*"))
	}
	keys := make([]string, 0, len(byDev))
	for k := range byDev {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		var match []string
		switch {
		case IsMAC(key):
			match = []string{"ether saddr " + key}
		case strings.HasPrefix(key, "ip:") && !strings.Contains(key, "::"):
			match = []string{"ip saddr " + strings.TrimPrefix(key, "ip:")}
		default:
			continue
		}
		for _, s := range byDev[key] {
			if all[s] {
				continue // blocked everywhere already
			}
			setOf[svcSet(s)] = true
			for _, m := range match {
				rules = append(rules, fmt.Sprintf("%s ip daddr @%s counter drop comment %q", m, svcSet(s), "svc|"+svcSet(s)+"|"+key))
			}
		}
	}
	for s := range setOf {
		sets = append(sets, s)
	}
	sort.Strings(sets)
	sort.Strings(rules)
	return sets, rules
}

// rebuilt fills the sets of a table just made from what is known (they start empty).
func (b *svcBlocks) rebuilt(sets []string, now int64) {
	b.mu.Lock()
	keep := map[string]bool{}
	for _, s := range sets {
		keep[s] = true
	}
	for s := range b.seen {
		if !keep[s] {
			delete(b.seen, s) // unblocked: forget its addresses
		}
	}
	b.inTable = map[string]map[netip.Addr]bool{}
	b.pending = map[string][]netip.Addr{}
	for s, m := range b.seen {
		for ip, t := range m {
			if now-t < svcKeep {
				b.pending[s] = append(b.pending[s], ip)
			}
		}
	}
	b.mu.Unlock()
}

// flush adds the addresses learnt since the last flush to the table's sets, and every 10 minutes takes out those
// not seen for svcKeep.
func (b *svcBlocks) flush(sets map[string]bool, now int64) error {
	b.mu.Lock()
	var cmds []string
	for s, ips := range b.pending {
		if !sets[s] || len(ips) == 0 {
			continue
		}
		in := b.inTable[s]
		if in == nil {
			in = map[netip.Addr]bool{}
			b.inTable[s] = in
		}
		var add []string
		for _, ip := range ips {
			if !in[ip] {
				in[ip] = true
				add = append(add, ip.String())
			}
		}
		if len(add) > 0 {
			cmds = append(cmds, fmt.Sprintf("add element inet %s %s { %s }", nftTable, s, strings.Join(add, ", ")))
		}
	}
	b.pending = map[string][]netip.Addr{}
	if now-b.pruned >= 600 {
		b.pruned = now
		for s, m := range b.seen {
			var del []string
			for ip, t := range m {
				if now-t >= svcKeep {
					delete(m, ip)
					if b.inTable[s][ip] {
						delete(b.inTable[s], ip)
						del = append(del, ip.String())
					}
				}
			}
			if len(del) > 0 && sets[s] {
				cmds = append(cmds, fmt.Sprintf("delete element inet %s %s { %s }", nftTable, s, strings.Join(del, ", ")))
			}
		}
	}
	b.mu.Unlock()
	if len(cmds) == 0 {
		return nil
	}
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(strings.Join(cmds, "\n") + "\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// addresses is how many addresses each blocked service has in the table.
func (b *svcBlocks) addresses(svc string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.inTable[svcSet(svc)])
}

// blockDrops reads what each blocked service's rule dropped: "set|device key" ("*" for every device) -> packets,
// bytes.
func (p *Policy) blockDrops() map[string][2]uint64 {
	out := map[string][2]uint64{}
	raw, err := exec.Command("nft", "-j", "list", "table", "inet", nftTable).Output()
	if err != nil {
		return out
	}
	var doc struct {
		Nftables []struct {
			Rule *struct {
				Comment string            `json:"comment"`
				Expr    []json.RawMessage `json:"expr"`
			} `json:"rule"`
		} `json:"nftables"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return out
	}
	for _, n := range doc.Nftables {
		if n.Rule == nil {
			continue
		}
		rest, ok := strings.CutPrefix(n.Rule.Comment, "svc|")
		if !ok {
			continue
		}
		for _, x := range n.Rule.Expr {
			var c struct {
				Counter *struct {
					Packets uint64 `json:"packets"`
					Bytes   uint64 `json:"bytes"`
				} `json:"counter"`
			}
			if json.Unmarshal(x, &c) == nil && c.Counter != nil {
				v := out[rest]
				v[0] += c.Counter.Packets
				v[1] += c.Counter.Bytes
				out[rest] = v
			}
		}
	}
	return out
}

type blockRow struct {
	Service    string `json:"service"`
	Device     string `json:"device"` // "": every device
	DeviceName string `json:"device_name"`
	Addresses  int    `json:"addresses"` // in its set now
	Packets    uint64 `json:"packets"`   // dropped since the table was last made
	Bytes      uint64 `json:"bytes"`
}

// apiBlocks lists every service block, with how many addresses it holds and what it dropped (the Blocked page).
func (e *Engine) apiBlocks(*ubus.Request) (any, error) {
	e.watched()
	cfg := e.Config()
	drops := e.policy.blockDrops()
	rows := []blockRow{}
	for _, s := range cfg.BlockServices {
		d := drops[svcSet(s)+"|*"]
		rows = append(rows, blockRow{Service: s, DeviceName: "", Addresses: e.blocks.addresses(s), Packets: d[0], Bytes: d[1]})
	}
	for key, dc := range cfg.Devices {
		for _, s := range dc.BlockServices {
			d := drops[svcSet(s)+"|"+key]
			rows = append(rows, blockRow{Service: s, Device: key, DeviceName: e.DeviceName(key), Addresses: e.blocks.addresses(s), Packets: d[0], Bytes: d[1]})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Service != rows[j].Service {
			return strings.ToLower(rows[i].Service) < strings.ToLower(rows[j].Service)
		}
		return rows[i].Device < rows[j].Device
	})
	return map[string]any{"blocks": rows}, nil
}

// apiBlockService blocks a service (its addresses) on one device, or on every device with device "", or lifts
// that block.
func (e *Engine) apiBlockService(r *ubus.Request) (any, error) {
	var q struct {
		Service string `json:"service"`
		Device  string `json:"device"`
		Block   bool   `json:"block"`
	}
	if err := r.Decode(&q); err != nil {
		return nil, err
	}
	q.Service = strings.TrimSpace(q.Service)
	if q.Service == "" || len(q.Service) > 128 || strings.ContainsAny(q.Service, "\n'") {
		return nil, ubus.Errorf("pick a service")
	}
	if q.Device == RouterKey {
		return nil, ubus.Errorf("the router's own traffic can't be blocked here")
	}
	cfg := e.Config()
	var sec string
	var have []string
	var cmds []string
	if q.Device == "" {
		sec, have = ConfigName+".main", cfg.BlockServices
	} else {
		dc := cfg.Devices[q.Device]
		switch {
		case dc != nil && dc.Section != "":
			sec, have = ConfigName+"."+dc.Section, dc.BlockServices
		case dc != nil:
			return nil, ubus.Errorf("this device is set in an unnamed section; edit /etc/config/wanscope")
		default:
			if e.Devs.Get(q.Device) == nil {
				return nil, ubus.Errorf("no such device")
			}
			sec = ConfigName + "." + sectionOf(q.Device)
			cmds = append(cmds, "set "+sec+"=device")
			if IsMAC(q.Device) {
				cmds = append(cmds, "set "+sec+".mac="+uci.Quote(q.Device))
			} else {
				cmds = append(cmds, "set "+sec+".key="+uci.Quote(q.Device))
			}
			// a section of its own counts as approved: one waiting for approval keeps waiting
			if d := e.Devs.Get(q.Device); cfg.Guest.Policy != "allow" && (d == nil || !d.Approved) {
				cmds = append(cmds, "set "+sec+".approved='0'")
			}
		}
	}
	on := false
	for _, s := range have {
		on = on || s == q.Service
	}
	switch {
	case q.Block && !on:
		cmds = append(cmds, "add_list "+sec+".block_service="+uci.Quote(q.Service))
	case !q.Block && on:
		cmds = append(cmds, "del_list "+sec+".block_service="+uci.Quote(q.Service))
	default:
		return map[string]any{"ok": true}, nil
	}
	if err := uci.Batch(ConfigName, cmds); err != nil {
		return nil, err
	}
	where := "every device"
	if q.Device != "" {
		where = e.DeviceName(q.Device)
	}
	e.log.Info("service block", "service", q.Service, "on", where, "blocked", q.Block)
	err := e.Reload()
	e.policy.dirty.Store(true)
	return map[string]any{"ok": true}, err
}
