package engine

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Alert struct {
	T    int64  `json:"t"`
	Kind string `json:"kind"` // new_device | cap | quota | burst | report | wan_down | test
	Key  string `json:"key,omitempty"`
	Text string `json:"text"`
	Sent string `json:"sent,omitempty"` // delivery result
}

type alertState struct {
	Log        []Alert          `json:"log"`
	Caps       map[string]bool  `json:"caps"`        // wan|cycle start|percent
	ReportDay  int64            `json:"report_day"`  // last daily report (day start)
	BurstUntil map[string]int64 `json:"burst_until"` // device -> quiet until
}

type Alerts struct {
	e    *Engine
	mu   sync.Mutex
	st   alertState
	path string
	up   map[string]bool // WAN state at the last check
}

func newAlerts(e *Engine) *Alerts {
	a := &Alerts{e: e, path: filepath.Join(e.Config().DataDir, "alerts.json"), up: map[string]bool{}}
	if raw, err := os.ReadFile(a.path); err == nil {
		json.Unmarshal(raw, &a.st)
	}
	if a.st.Caps == nil {
		a.st.Caps = map[string]bool{}
	}
	if a.st.BurstUntil == nil {
		a.st.BurstUntil = map[string]int64{}
	}
	return a
}

func (a *Alerts) save() {
	a.mu.Lock()
	raw, err := json.Marshal(a.st)
	a.mu.Unlock()
	if err == nil {
		os.WriteFile(a.path+".tmp", raw, 0o644)
		os.Rename(a.path+".tmp", a.path)
	}
}

func (a *Alerts) Log(n int) []Alert {
	a.mu.Lock()
	defer a.mu.Unlock()
	l := a.st.Log
	if n > 0 && len(l) > n {
		l = l[len(l)-n:]
	}
	out := make([]Alert, len(l))
	for i := range l {
		out[i] = l[len(l)-1-i]
	}
	return out
}

func (a *Alerts) emit(kind, key, text string, deliver bool) {
	al := Alert{T: time.Now().Unix(), Kind: kind, Key: key, Text: text}
	a.mu.Lock()
	a.st.Log = append(a.st.Log, al)
	if len(a.st.Log) > 500 {
		a.st.Log = a.st.Log[len(a.st.Log)-500:]
	}
	idx := len(a.st.Log) - 1
	a.mu.Unlock()
	a.e.log.Info("alert", "kind", kind, "text", text)
	if !deliver {
		return
	}
	go func() {
		res := a.deliver(text)
		a.mu.Lock()
		if idx < len(a.st.Log) && a.st.Log[idx].T == al.T && a.st.Log[idx].Text == text {
			a.st.Log[idx].Sent = res
		}
		a.mu.Unlock()
		a.save()
	}()
	a.save()
}

// deliver sends a message by the configured method; it returns a short
// result for the log.
func (a *Alerts) deliver(text string) string {
	n := a.e.Config().Notify
	host := "wanscope"
	switch n.Method {
	case "telegram":
		if n.Token == "" || n.Chat == "" {
			return "not sent: set the bot token and chat id"
		}
		form := url.Values{"chat_id": {n.Chat}, "text": {text}, "disable_web_page_preview": {"true"}}
		cl := &http.Client{Timeout: 20 * time.Second}
		resp, err := cl.PostForm("https://api.telegram.org/bot"+n.Token+"/sendMessage", form)
		if err != nil {
			return "not sent: " + strings.ReplaceAll(err.Error(), n.Token, "…")
		}
		defer resp.Body.Close()
		var r struct {
			OK          bool   `json:"ok"`
			Description string `json:"description"`
		}
		json.NewDecoder(resp.Body).Decode(&r)
		if !r.OK {
			return "not sent: " + r.Description
		}
		return "sent"
	case "command":
		f := strings.Fields(n.Command)
		if len(f) == 0 {
			return "not sent: no command"
		}
		cmd := exec.Command(f[0], append(f[1:], text)...)
		cmd.Env = append(os.Environ(), "WANSCOPE=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "not sent: " + strings.TrimSpace(err.Error()+" "+string(out))
		}
		return "sent"
	}
	_ = host
	return ""
}

// Test sends a test message and returns the result.
func (a *Alerts) Test() string {
	text := "✅ wanscope on " + a.e.hostname() + ": notifications work."
	res := a.deliver(text)
	a.mu.Lock()
	a.st.Log = append(a.st.Log, Alert{T: time.Now().Unix(), Kind: "test", Text: text, Sent: res})
	a.mu.Unlock()
	a.save()
	return res
}

func (a *Alerts) newDevice(key string, now int64) {
	if now-a.e.started < 120 {
		return // the first look after start finds everyone
	}
	n := a.e.Config().Notify
	d := a.e.Devs.Get(key)
	name := a.e.DeviceName(key)
	ip := ""
	if d != nil {
		for k := range d.IPs {
			ip = k
		}
	}
	text := fmt.Sprintf("🆕 New device on the network: %s", name)
	if ip != "" && !strings.Contains(name, ip) {
		text += " (" + ip + ")"
	}
	if RandomMAC(key) {
		text += ", private Wi-Fi address"
	}
	if g := a.e.Config().Guest; g.Policy == "block" {
		text += ". It has no internet until you approve it."
	} else if g.Policy == "limit" {
		text += ". It gets guest speed until you approve it."
	}
	a.emit("new_device", key, text, n.NewDevice && n.Method != "none")
}

func (a *Alerts) quota(key string, q Quota, used uint64, now int64) {
	n := a.e.Config().Notify
	what := map[string]string{"block": "Its internet is off", "limit": "It is slowed down", "alert": ""}[q.Action]
	text := fmt.Sprintf("📊 %s used its %s %s quota (%s of %s).", a.e.DeviceName(key), q.Period,
		map[string]string{"total": "data", "down": "download", "up": "upload"}[q.Dir], HumanBytes(used), HumanBytes(uint64(q.MB*1e6)))
	if what != "" {
		text += " " + what + " until the " + q.Period + " ends."
	}
	a.emit("quota", key, text, n.QuotaAlerts && n.Method != "none")
}

// check runs the periodic alerts: caps, bursts, WAN state, daily report.
func (a *Alerts) check(now int64) {
	cfg := a.e.Config()
	n := cfg.Notify
	deliver := n.Method != "none"
	topo := a.e.Topo()
	for _, w := range topo.WANs() {
		// WAN up/down
		if prev, ok := a.up[w.Name]; ok && prev != w.Up && n.OfflineAlerts {
			if w.Up {
				a.emit("wan_down", w.Name, fmt.Sprintf("✅ %s is back online.", a.e.WANLabel(w.Name)), deliver)
			} else {
				a.emit("wan_down", w.Name, fmt.Sprintf("⚠️ %s went offline.", a.e.WANLabel(w.Name)), deliver)
			}
		}
		a.up[w.Name] = w.Up
	}
	// data plans: each bundle (or cycle) alerts once per threshold
	loc := a.e.Store.Location()
	for _, p := range cfg.Plans {
		st := a.e.PlanStatus(p, now, false)
		if st.Size == 0 {
			continue
		}
		pct := float64(st.Used) / float64(st.Size) * 100
		for _, th := range n.CapPercents {
			if pct < float64(th) {
				continue
			}
			k := fmt.Sprintf("plan|%s|%d|%d", p.ID, st.Start, th)
			a.mu.Lock()
			done := a.st.Caps[k]
			a.st.Caps[k] = true
			a.mu.Unlock()
			if done {
				continue
			}
			var text string
			if st.Left <= 0 {
				text = fmt.Sprintf("📶 %s (%s) has used all of its %s.", p.Name, strings.Join(p.Ifaces, ", "), HumanBytes(st.Size))
				if p.Type == "prepaid" {
					text += " When you buy more, press “I bought more” on wanscope's Uplinks page so the count starts again."
				}
			} else {
				text = fmt.Sprintf("📶 %s (%s) used %d%% of its %s: %s left.", p.Name, strings.Join(p.Ifaces, ", "), th, HumanBytes(st.Size), HumanBytes(uint64(st.Left)))
				if st.RunsOut > 0 {
					text += fmt.Sprintf(" At the recent pace (%s a day) it runs out around %s.", HumanBytes(uint64(st.Pace)), time.Unix(st.RunsOut, 0).In(loc).Format("Mon 2 Jan 15:04"))
				}
			}
			a.emit("cap", p.ID, text, deliver)
		}
	}
	_ = topo
	// bursts: a device using a lot in a short time
	if n.BurstMB > 0 {
		win := int64(n.BurstMinutes) * 60
		tot := a.e.Store.Totals([]string{"d:*"}, now-win, now+1)
		for k, c := range tot {
			key := strings.TrimPrefix(k, "d:")
			if float64(c.Total()) < n.BurstMB*1e6 {
				continue
			}
			a.mu.Lock()
			quiet := a.st.BurstUntil[key] > now
			if !quiet {
				a.st.BurstUntil[key] = now + win
			}
			a.mu.Unlock()
			if quiet {
				continue
			}
			a.emit("burst", key, fmt.Sprintf("⚡ %s used %s in %d minutes (%s down, %s up).", a.e.DeviceName(key),
				HumanBytes(c.Total()), n.BurstMinutes, HumanBytes(c.Down), HumanBytes(c.Up)), deliver)
		}
	}
	// daily report for yesterday
	if n.DailyReport {
		loc := a.e.Store.Location()
		lt := time.Unix(now, 0).In(loc)
		at, err := parseClock(n.ReportTime)
		today := a.e.Store.DayStart(now)
		a.mu.Lock()
		due := err == nil && lt.Hour()*60+lt.Minute() >= at && a.st.ReportDay < today
		if due {
			a.st.ReportDay = today
		}
		a.mu.Unlock()
		if due {
			y := time.Unix(today, 0).In(loc).AddDate(0, 0, -1).Unix()
			a.emit("report", "", a.e.Report(y, today), deliver)
		}
	}
}

// Report summarises a day: uplinks and the busiest devices.
func (e *Engine) Report(from, to int64) string {
	var b strings.Builder
	day := time.Unix(from, 0).In(e.Store.Location()).Format("Mon 2 Jan")
	fmt.Fprintf(&b, "📈 %s on %s\n", day, e.hostname())
	tot := e.Store.Totals([]string{"i:*", "d:*"}, from, to)
	var all uint64
	for _, w := range e.Topo().WANs() {
		c := tot["i:"+w.Name]
		if c.Total() == 0 {
			continue
		}
		if w.Kind == "uplink" {
			all += c.Total()
		}
		fmt.Fprintf(&b, "%s: %s (↓%s ↑%s)\n", e.WANLabel(w.Name), HumanBytes(c.Total()), HumanBytes(c.Down), HumanBytes(c.Up))
	}
	fmt.Fprintf(&b, "All uplinks: %s\n", HumanBytes(all))
	for _, p := range e.Config().Plans {
		st := e.PlanStatus(p, time.Now().Unix(), false)
		fmt.Fprintf(&b, "%s: %s left of %s\n", p.Name, HumanBytes(uint64(max(0, st.Left))), HumanBytes(st.Size))
	}
	type kv struct {
		k string
		c uint64
	}
	var devs []kv
	for k, c := range tot {
		if strings.HasPrefix(k, "d:") {
			devs = append(devs, kv{strings.TrimPrefix(k, "d:"), c.Total()})
		}
	}
	sort.Slice(devs, func(i, j int) bool { return devs[i].c > devs[j].c })
	for i, d := range devs {
		if i == 5 {
			break
		}
		fmt.Fprintf(&b, "%d. %s %s\n", i+1, e.DeviceName(d.k), HumanBytes(d.c))
	}
	return strings.TrimSpace(b.String())
}

func HumanBytes(v uint64) string {
	f := float64(v)
	switch {
	case f >= 1e12:
		return fmt.Sprintf("%.2f TB", f/1e12)
	case f >= 1e9:
		return fmt.Sprintf("%.2f GB", f/1e9)
	case f >= 1e6:
		return fmt.Sprintf("%.1f MB", f/1e6)
	case f >= 1e3:
		return fmt.Sprintf("%.0f kB", f/1e3)
	}
	return fmt.Sprintf("%d B", v)
}
