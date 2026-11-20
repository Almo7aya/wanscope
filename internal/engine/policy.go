package engine

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Almo7aya/wanscope/internal/store"
)

const nftTable = "wanscope"

// Effect is what applies to a device right now.
type Effect struct {
	Blocked  bool     `json:"blocked"`
	Down     int      `json:"down"` // kB/s (1000 bytes), 0 = no limit
	Up       int      `json:"up"`
	Reasons  []string `json:"reasons,omitempty"`
	Until    int64    `json:"until,omitempty"` // end of a pause
	Guest    bool     `json:"guest,omitempty"`
	QuotaHit []string `json:"quota_hit,omitempty"`
}

func (e Effect) Active() bool { return e.Blocked || e.Down > 0 || e.Up > 0 }

func (e *Effect) limit(down, up int, why string) {
	if down > 0 && (e.Down == 0 || down < e.Down) {
		e.Down = down
	}
	if up > 0 && (e.Up == 0 || up < e.Up) {
		e.Up = up
	}
	e.Reasons = append(e.Reasons, why)
}

// Policy turns device settings into an nftables table of its own.
type Policy struct {
	e        *Engine
	dirty    atomic.Bool
	mu       sync.RWMutex
	effects  map[string]Effect
	applied  string
	lastEval int64
	lastSeen int64 // last check that the table still exists
	err      string
	drops    map[string]store.Counter
	hits     map[string]bool // quota hits already reported (key|quota|period start)
}

func newPolicy(e *Engine) *Policy {
	p := &Policy{e: e, effects: map[string]Effect{}, drops: map[string]store.Counter{}, hits: map[string]bool{}}
	p.dirty.Store(true)
	return p
}

func (p *Policy) Effect(key string) Effect {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.effects[key]
}

func (p *Policy) Error() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.err
}

func (p *Policy) Drops(key string) store.Counter {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.drops[key]
}

// periodStart is the local start of a quota period containing t.
func periodStart(loc *time.Location, period string, t int64) int64 {
	lt := time.Unix(t, 0).In(loc)
	day := time.Date(lt.Year(), lt.Month(), lt.Day(), 0, 0, 0, 0, loc)
	switch period {
	case "week":
		wd := (int(lt.Weekday()) + 6) % 7 // Monday = 0
		return day.AddDate(0, 0, -wd).Unix()
	case "month":
		return time.Date(lt.Year(), lt.Month(), 1, 0, 0, 0, 0, loc).Unix()
	}
	return day.Unix()
}

func (p *Policy) tick(now int64) {
	if !p.dirty.Load() && now-p.lastEval < 5 {
		return
	}
	p.dirty.Store(false)
	p.lastEval = now
	effects := p.evaluate(now)
	script := p.script(effects, now)
	p.mu.Lock()
	p.effects = effects
	changed := script != p.applied
	recheck := now-p.lastSeen >= 30
	p.mu.Unlock()
	if !changed && recheck && script != "" {
		// a firewall restart may have removed the table
		p.lastSeen = now
		if exec.Command("nft", "list", "table", "inet", nftTable).Run() != nil {
			changed = true
		}
		p.readDrops()
	}
	if !changed {
		return
	}
	err := p.apply(script)
	p.mu.Lock()
	if err != nil {
		p.err = err.Error()
		p.e.log.Warn("nft", "err", err)
	} else {
		p.err = ""
		p.applied = script
	}
	p.mu.Unlock()
}

func (p *Policy) evaluate(now int64) map[string]Effect {
	cfg := p.e.Config()
	loc := p.e.Store.Location()
	lt := time.Unix(now, 0).In(loc)
	out := map[string]Effect{}
	devs := p.e.Devs.All()
	known := map[string]bool{}
	for _, d := range devs {
		known[d.Key] = true
	}
	keys := make([]string, 0, len(cfg.Devices))
	for k := range cfg.Devices {
		keys = append(keys, k)
	}
	for _, d := range devs {
		if cfg.Devices[d.Key] == nil {
			keys = append(keys, d.Key)
		}
	}
	for _, key := range keys {
		var eff Effect
		dc := cfg.Devices[key]
		if cfg.Guest.Policy == "block" || cfg.Guest.Policy == "limit" {
			d := p.e.Devs.Get(key)
			approved := (dc != nil && dc.Approved) || (d != nil && (d.Approved || (cfg.Guest.Since > 0 && d.FirstSeen < cfg.Guest.Since)))
			if d != nil && !approved && IsMAC(key) {
				eff.Guest = true
				if cfg.Guest.Policy == "block" {
					eff.Blocked = true
					eff.Reasons = append(eff.Reasons, "new device: waiting for approval")
				} else {
					eff.limit(cfg.Guest.Down, cfg.Guest.Up, "new device: guest speed")
				}
			}
		}
		if dc != nil {
			if dc.Block {
				eff.Blocked = true
				eff.Reasons = append(eff.Reasons, "blocked")
			}
			if dc.PauseUntil > now {
				eff.Blocked = true
				eff.Until = dc.PauseUntil
				eff.Reasons = append(eff.Reasons, "paused")
			}
			if dc.Down > 0 || dc.Up > 0 {
				eff.limit(dc.Down, dc.Up, "speed limit")
			}
			for _, sc := range dc.Schedules {
				if !sc.Active(lt) {
					continue
				}
				if sc.Action == "block" {
					eff.Blocked = true
					eff.Reasons = append(eff.Reasons, "schedule: "+sc.Raw)
				} else {
					eff.limit(sc.Down, sc.Up, "schedule: "+sc.Raw)
				}
			}
			for _, q := range dc.Quotas {
				start := periodStart(loc, q.Period, now)
				used := p.e.Store.Totals([]string{"d:" + key}, start, now+1)["d:"+key]
				var v uint64
				switch q.Dir {
				case "down":
					v = used.Down
				case "up":
					v = used.Up
				default:
					v = used.Total()
				}
				if float64(v) < q.MB*1e6 {
					continue
				}
				eff.QuotaHit = append(eff.QuotaHit, q.Raw)
				hk := fmt.Sprintf("%s|%s|%d", key, q.Raw, start)
				if !p.hits[hk] {
					p.hits[hk] = true
					p.e.alerts.quota(key, q, v, now)
				}
				switch q.Action {
				case "block":
					eff.Blocked = true
					eff.Reasons = append(eff.Reasons, "quota used up: "+q.Raw)
				case "limit":
					eff.limit(q.Down, q.Up, "quota used up: "+q.Raw)
				}
			}
		}
		if eff.Active() || len(eff.QuotaHit) > 0 {
			out[key] = eff
		}
	}
	if len(p.hits) > 10000 {
		p.hits = map[string]bool{}
	}
	return out
}

// script renders the table; "" when nothing applies.
func (p *Policy) script(effects map[string]Effect, now int64) string {
	keys := make([]string, 0, len(effects))
	for k, e := range effects {
		if e.Active() {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	var rules []string
	for _, key := range keys {
		eff := effects[key]
		var v4, v6 []string
		for _, ip := range p.e.Devs.IPsOf(key, now) {
			if ip.Is4() {
				v4 = append(v4, ip.String())
			} else {
				v6 = append(v6, ip.String())
			}
		}
		var upMatch string
		switch {
		case IsMAC(key):
			upMatch = "ether saddr " + key
		case strings.HasPrefix(key, "ip:"):
			ip := strings.TrimPrefix(key, "ip:")
			if strings.Contains(ip, ":") {
				upMatch, v6 = "ip6 saddr "+ip, []string{ip}
			} else {
				upMatch, v4 = "ip saddr "+ip, []string{ip}
			}
		default:
			continue
		}
		var downMatches []string
		if len(v4) > 0 {
			downMatches = append(downMatches, "ip daddr { "+strings.Join(v4, ", ")+" }")
		}
		if len(v6) > 0 {
			downMatches = append(downMatches, "ip6 daddr { "+strings.Join(v6, ", ")+" }")
		}
		if eff.Blocked {
			rules = append(rules, fmt.Sprintf("%s counter drop comment %q", upMatch, key+"|up"))
			for _, m := range downMatches {
				rules = append(rules, fmt.Sprintf("%s counter drop comment %q", m, key+"|down"))
			}
			continue
		}
		if eff.Up > 0 {
			rules = append(rules, fmt.Sprintf("%s %s counter drop comment %q", upMatch, limitExpr(eff.Up), key+"|up"))
		}
		if eff.Down > 0 {
			for _, m := range downMatches {
				rules = append(rules, fmt.Sprintf("%s %s counter drop comment %q", m, limitExpr(eff.Down), key+"|down"))
			}
		}
	}
	if len(rules) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "table inet %s {\n\tchain forward_limits {\n\t\ttype filter hook forward priority filter - 5; policy accept;\n", nftTable)
	for _, r := range rules {
		b.WriteString("\t\t" + r + "\n")
	}
	b.WriteString("\t}\n}\n")
	return b.String()
}

// limitExpr polices to kB/s with a quarter second of burst.
func limitExpr(kB int) string {
	bps := kB * 1000
	burst := max(bps/4, 32768)
	return fmt.Sprintf("limit rate over %d bytes/second burst %d bytes", bps, burst)
}

func (p *Policy) apply(script string) error {
	full := fmt.Sprintf("table inet %s\ndelete table inet %s\n%s", nftTable, nftTable, script)
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(full)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	if script != "" {
		p.e.log.Info("limits applied", "rules", strings.Count(script, "comment"))
	}
	return nil
}

// clear removes the table (the daemon is stopping).
func (p *Policy) clear() {
	exec.Command("nft", "delete", "table", "inet", nftTable).Run()
}

// readDrops reads how much each device's rules dropped.
func (p *Policy) readDrops() {
	out, err := exec.Command("nft", "-j", "list", "table", "inet", nftTable).Output()
	if err != nil {
		return
	}
	var doc struct {
		Nftables []struct {
			Rule *struct {
				Comment string            `json:"comment"`
				Expr    []json.RawMessage `json:"expr"`
			} `json:"rule"`
		} `json:"nftables"`
	}
	if json.Unmarshal(out, &doc) != nil {
		return
	}
	drops := map[string]store.Counter{}
	for _, n := range doc.Nftables {
		if n.Rule == nil {
			continue
		}
		key, dir, ok := strings.Cut(n.Rule.Comment, "|")
		if !ok {
			continue
		}
		for _, x := range n.Rule.Expr {
			var c struct {
				Counter *struct {
					Bytes uint64 `json:"bytes"`
				} `json:"counter"`
			}
			if json.Unmarshal(x, &c) == nil && c.Counter != nil {
				d := drops[key]
				if dir == "up" {
					d.Up += c.Counter.Bytes
				} else {
					d.Down += c.Counter.Bytes
				}
				drops[key] = d
			}
		}
	}
	p.mu.Lock()
	p.drops = drops
	p.mu.Unlock()
}
