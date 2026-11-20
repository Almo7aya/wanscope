package engine

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Almo7aya/wanscope/internal/names"
	"github.com/Almo7aya/wanscope/internal/store"
	"github.com/Almo7aya/wanscope/internal/sys"
	"github.com/Almo7aya/wanscope/internal/ubus"
	"github.com/Almo7aya/wanscope/internal/uci"
)

func (e *Engine) ubusObject() *ubus.Object {
	s, i, b, a := ubus.ArgString, ubus.ArgInt, ubus.ArgBool, ubus.ArgArray
	arg := func(n string, t int) ubus.Arg { return ubus.Arg{Name: n, Type: t} }
	return &ubus.Object{Name: "wanscope", Methods: []ubus.Method{
		{Name: "status", Handler: e.apiStatus},
		{Name: "live", Args: []ubus.Arg{arg("since", i), arg("window", i), arg("devices", b)}, Handler: e.apiLive},
		{Name: "devices", Handler: e.apiDevices},
		{Name: "device", Args: []ubus.Arg{arg("key", s), arg("from", i), arg("to", i)}, Handler: e.apiDevice},
		{Name: "wans", Handler: e.apiWANs},
		{Name: "flows", Args: []ubus.Arg{arg("device", s), arg("wan", s), arg("limit", i)}, Handler: e.apiFlows},
		{Name: "history", Args: []ubus.Arg{arg("keys", a), arg("from", i), arg("to", i), arg("step", i)}, Handler: e.apiHistory},
		{Name: "top", Args: []ubus.Arg{arg("groups", a), arg("from", i), arg("to", i), arg("limit", i)}, Handler: e.apiTop},
		{Name: "usage", Args: []ubus.Arg{arg("keys", a), arg("from", i), arg("to", i)}, Handler: e.apiUsage},
		{Name: "alerts", Args: []ubus.Arg{arg("limit", i)}, Handler: e.apiAlerts},
		{Name: "report", Args: []ubus.Arg{arg("from", i), arg("to", i)}, Handler: e.apiReport},
		{Name: "notify_test", Handler: e.apiNotifyTest},
		{Name: "set_device", Args: []ubus.Arg{arg("key", s), arg("name", s), arg("icon", s), arg("block", b), arg("pause", i),
			arg("down", i), arg("up", i), arg("schedules", a), arg("quotas", a), arg("approved", b)}, Handler: soft(e.apiSetDevice)},
		{Name: "set_wan", Args: []ubus.Arg{arg("name", s), arg("label", s), arg("kind", s), arg("hidden", b)}, Handler: soft(e.apiSetWAN)},
		{Name: "plans", Handler: e.apiPlans},
		{Name: "set_plan", Args: []ubus.Arg{arg("id", s), arg("name", s), arg("type", s), arg("size_gb", s), arg("ifaces", a), arg("cycle_day", i), arg("valid_days", i), arg("used_gb", s)}, Handler: soft(e.apiSetPlan)},
		{Name: "topup", Args: []ubus.Arg{arg("id", s), arg("size_gb", s), arg("mode", s)}, Handler: soft(e.apiTopup)},
		{Name: "calibrate", Args: []ubus.Arg{arg("id", s), arg("left_gb", s)}, Handler: soft(e.apiCalibrate)},
		{Name: "delete_plan", Args: []ubus.Arg{arg("id", s)}, Handler: soft(e.apiDeletePlan)},
		{Name: "set_guest", Args: []ubus.Arg{arg("policy", s), arg("down", i), arg("up", i)}, Handler: soft(e.apiSetGuest)},
		{Name: "forget", Args: []ubus.Arg{arg("key", s)}, Handler: soft(e.apiForget)},
		{Name: "set_rule", Args: []ubus.Arg{arg("name", s), arg("category", s), arg("domain", s), arg("cidr", s), arg("port", s)}, Handler: soft(e.apiSetRule)},
		{Name: "block_service", Args: []ubus.Arg{arg("service", s), arg("device", s), arg("block", b)}, Handler: soft(e.apiBlockService)},
		{Name: "blocks", Handler: e.apiBlocks},
		{Name: "reload", Handler: func(*ubus.Request) (any, error) { return map[string]any{"ok": true}, e.Reload() }},
	}}
}

func (e *Engine) hostname() string { return sys.Hostname() }

// soft turns a write method's error into a reply LuCI can show (it drops
// the data of failed calls).
func soft(h ubus.Handler) ubus.Handler {
	return func(r *ubus.Request) (any, error) {
		res, err := h(r)
		if err != nil {
			return map[string]any{"ok": false, "error": err.Error()}, nil
		}
		return res, nil
	}
}

// DeviceName is the best name for a device: the one given in wanscope,
// a static DHCP name, the name it asked DHCP for, or its address.
func (e *Engine) DeviceName(key string) string {
	if dc := e.Config().Devices[key]; dc != nil && dc.Name != "" {
		return dc.Name
	}
	return e.autoName(key)
}

// autoName is a device's name without the one given in wanscope.
func (e *Engine) autoName(key string) string {
	if key == RouterKey {
		return e.hostname() + " (router)"
	}
	if n := e.Devs.StaticName(key); n != "" {
		return n
	}
	d := e.Devs.Get(key)
	if d != nil && d.Hostname != "" {
		return d.Hostname
	}
	if d != nil && d.Found != "" {
		return d.Found
	}
	if d != nil {
		for ip := range d.IPs {
			if a, err := parseAddr(ip); err == nil {
				if w := e.Topo().Gateway(a); w != "" {
					return e.WANLabel(w) + " modem"
				}
			}
		}
		ips := e.Devs.IPsOf(key, time.Now().Unix())
		if len(ips) > 0 {
			for _, ip := range ips {
				if ip.Is4() {
					return ip.String()
				}
			}
			return ips[0].String()
		}
	}
	return strings.TrimPrefix(key, "ip:")
}

func (e *Engine) WANLabel(name string) string {
	if wc := e.Config().WANs[name]; wc != nil && wc.Label != "" {
		return wc.Label
	}
	return name
}

func csv(pts []store.Counter) string {
	var b strings.Builder
	b.Grow(len(pts) * 8)
	for i, p := range pts {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatUint(p.Down, 10))
		b.WriteByte(',')
		b.WriteString(strconv.FormatUint(p.Up, 10))
	}
	return b.String()
}

func (e *Engine) apiStatus(*ubus.Request) (any, error) {
	e.watched()
	cfg := e.Config()
	now := time.Now()
	_, off := now.In(e.Store.Location()).Zone()
	e.mu.RLock()
	nflows, tracked, cost, pcost, warn, sdevs := e.nflows, len(e.flows), e.tickCost, e.pollCost, append([]string(nil), e.warnings...), e.sniffDevs
	e.mu.RUnlock()
	disk, _ := e.Store.Usage()
	st := e.names.Stats()
	var pk uint64
	if e.sniffer != nil {
		pk = e.sniffer.Packets.Load()
	}
	type ifaceInfo struct {
		Name    string `json:"name"`
		Label   string `json:"label"`
		Dev     string `json:"dev"`
		Role    string `json:"role"`
		Kind    string `json:"kind"`
		Up      bool   `json:"up"`
		Gateway string `json:"gateway,omitempty"`
		Plan    string `json:"plan,omitempty"`
		Hidden  bool   `json:"hidden"`
	}
	var ifaces []ifaceInfo
	for _, i := range e.Topo().Ifaces {
		ii := ifaceInfo{Name: i.Name, Label: e.WANLabel(i.Name), Dev: i.Dev, Role: i.Role, Kind: i.Kind, Up: i.Up}
		if p := e.planOf(i.Name); p != nil {
			ii.Plan = p.Name
		}
		if i.Gateway.IsValid() {
			ii.Gateway = i.Gateway.String()
		}
		if wc := cfg.WANs[i.Name]; wc != nil {
			ii.Hidden = wc.Hidden
		}
		ifaces = append(ifaces, ii)
	}
	return map[string]any{
		"version": Version, "host": e.hostname(), "now": now.Unix(), "started": e.started,
		"tz": e.Store.Location().String(), "offset": off, "interval": int(cfg.Interval / time.Second),
		"live_seconds": cfg.LiveSeconds, "conntrack": nflows, "tracked": tracked, "tick_ms": float64(cost.Microseconds()) / 1000, "poll_ms": float64(pcost.Microseconds()) / 1000,
		"devices": len(e.Devs.All()), "disk": disk, "warnings": warn, "policy_error": e.policy.Error(),
		"sniff":  map[string]any{"on": cfg.Sniff, "devs": sdevs, "dns": st.DNS, "tls": st.TLS, "quic": st.QUIC, "packets": pk},
		"ifaces": ifaces, "notify": cfg.Notify.Method, "guest": cfg.Guest.Policy, "data": e.dataStatus(),
		"block_services": append([]string{}, cfg.BlockServices...),
	}, nil
}

// watched marks a page as open: connections are read every second for a while.
func (e *Engine) watched() { e.viewerUntil.Store(time.Now().Unix() + 15) }

func (e *Engine) apiLive(r *ubus.Request) (any, error) {
	e.watched()
	var q struct {
		Since   int64 `json:"since"`
		Window  int64 `json:"window"`
		Devices *bool `json:"devices"`
	}
	if err := r.Decode(&q); err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	if q.Window <= 0 {
		q.Window = 300
	}
	if q.Since < now-q.Window {
		q.Since = now - q.Window
	}
	keys := []string{"i:*"}
	if q.Devices == nil || *q.Devices {
		keys = append(keys, "d:*")
	}
	from, to, data := e.Store.Live(keys, q.Since)
	ifaces := map[string]string{}
	devs := map[string]string{}
	for k, pts := range data {
		busy := false
		for _, p := range pts {
			if p.Up+p.Down > 0 {
				busy = true
				break
			}
		}
		if name, ok := strings.CutPrefix(k, "i:"); ok {
			ifaces[name] = csv(pts)
		} else if key, ok := strings.CutPrefix(k, "d:"); ok && busy {
			devs[key] = csv(pts)
		}
	}
	return map[string]any{"from": from, "to": to, "ifaces": ifaces, "devices": devs}, nil
}

type devInfo struct {
	Key       string                   `json:"key"`
	MAC       string                   `json:"mac,omitempty"`
	Name      string                   `json:"name"`
	Custom    string                   `json:"custom_name,omitempty"`
	Auto      string                   `json:"auto_name"`
	Hostname  string                   `json:"hostname,omitempty"`
	Icon      string                   `json:"icon,omitempty"`
	IPs       []string                 `json:"ips"`
	Online    bool                     `json:"online"`
	Private   bool                     `json:"private,omitempty"`
	Modem     string                   `json:"modem,omitempty"`
	FirstSeen int64                    `json:"first_seen"`
	LastSeen  int64                    `json:"last_seen"`
	Rate      store.Counter            `json:"rate"`
	Today     store.Counter            `json:"today"`
	Month     store.Counter            `json:"month"`
	Local     store.Counter            `json:"local"`
	WANs      map[string]store.Counter `json:"wans"`
	Top       []string                 `json:"top"`
	Now       []nowService             `json:"now,omitempty"` // what it uses right now (the last 5 seconds), busiest first
	Flows     int                      `json:"flows"`
	Effect    Effect                   `json:"effect"`
	Policy    *devPolicy               `json:"policy,omitempty"`
	Drops     store.Counter            `json:"drops"`
	Approved  bool                     `json:"approved"`
}

// nowService is a service a device used in the last few seconds, with its rate now (0: a pause between bursts).
type nowService struct {
	Name     string `json:"name"`
	Category string `json:"category,omitempty"`
	Down     uint64 `json:"down"`
	Up       uint64 `json:"up"`
	Conns    int    `json:"conns"`
}

// nowWindow is how recent a connection's traffic must be to count as "now".
const nowWindow = 5

// flowLabel names what a connection talks to: its service, else its site, its server name or its address.
func flowLabel(f *FlowState) string {
	switch {
	case f.Service != "":
		return f.Service
	case f.Site != "":
		return f.Site
	case f.Host != "":
		return f.Host
	}
	return f.Remote.Addr().String()
}

type devPolicy struct {
	Block      bool     `json:"block"`
	PauseUntil int64    `json:"pause_until"`
	Down       int      `json:"down"`
	Up         int      `json:"up"`
	Schedules  []string `json:"schedules"`
	Quotas     []string `json:"quotas"`
	Services   []string `json:"block_services"` // services blocked on this device (their addresses)
}

func (e *Engine) devInfos(keys map[string]bool) []*devInfo {
	cfg := e.Config()
	now := time.Now().Unix()
	today := e.Store.DayStart(now)
	lt := time.Unix(now, 0).In(e.Store.Location())
	month := time.Date(lt.Year(), lt.Month(), 1, 0, 0, 0, 0, e.Store.Location()).Unix()
	td := e.Store.Totals([]string{"d:*", "dw:*", "l:*"}, today, now+1)
	md := e.Store.Totals([]string{"d:*"}, month, now+1)
	labels := e.Store.Labels([]string{"*"}, today, now+1)
	e.mu.RLock()
	rates := e.rates
	nflows := map[string]int{}
	busy := map[string]map[string]*nowService{}
	for _, f := range e.flows {
		if !f.LocalOnly && now-f.Last < 60 {
			nflows[f.Dev]++
		}
		if f.LocalOnly || f.Dev == "" || now-f.Last >= nowWindow {
			continue
		}
		m := busy[f.Dev]
		if m == nil {
			m = map[string]*nowService{}
			busy[f.Dev] = m
		}
		name := flowLabel(f)
		s := m[name]
		if s == nil {
			s = &nowService{Name: name, Category: f.Category}
			m[name] = s
		}
		s.Down += f.RateDown
		s.Up += f.RateUp
		s.Conns++
	}
	e.mu.RUnlock()

	var out []*devInfo
	for _, d := range append(e.Devs.All(), &Device{Key: RouterKey}) {
		if keys != nil && !keys[d.Key] {
			continue
		}
		dc := cfg.Devices[d.Key]
		recent := now-d.LastSeen < 30*86400 || now-d.FirstSeen < 86400 || dc != nil || d.Key == RouterKey
		if keys == nil && !recent {
			continue
		}
		di := &devInfo{Key: d.Key, MAC: d.MAC, Name: e.DeviceName(d.Key), Hostname: d.Hostname, FirstSeen: d.FirstSeen,
			LastSeen: d.LastSeen, Rate: rates["d:"+d.Key], Today: td["d:"+d.Key], Month: md["d:"+d.Key],
			Local: td["l:"+d.Key], WANs: map[string]store.Counter{}, Flows: nflows[d.Key], Effect: e.policy.Effect(d.Key),
			Drops: e.policy.Drops(d.Key), Private: RandomMAC(d.Key), Approved: d.Approved || d.Key == RouterKey, IPs: []string{}}
		for _, ip := range e.Devs.IPsOf(d.Key, now) {
			di.IPs = append(di.IPs, ip.String())
			if w := e.Topo().Gateway(ip); w != "" {
				di.Modem = w
			}
		}
		di.Online = d.Key == RouterKey || now-d.LastSeen < cfg.OfflineSecs || now-d.Reachable < 60
		di.Auto = e.autoName(d.Key)
		if dc != nil {
			di.Custom = dc.Name
			di.Icon = dc.Icon
			di.Approved = di.Approved || dc.Approved
			p := &devPolicy{Block: dc.Block, PauseUntil: dc.PauseUntil, Down: dc.Down, Up: dc.Up, Schedules: []string{}, Quotas: []string{},
				Services: append([]string{}, dc.BlockServices...)}
			for _, s := range dc.Schedules {
				p.Schedules = append(p.Schedules, s.Raw)
			}
			for _, q := range dc.Quotas {
				p.Quotas = append(p.Quotas, q.Raw)
			}
			di.Policy = p
		}
		prefix := "dw:" + d.Key + "|"
		for k, c := range td {
			if w, ok := strings.CutPrefix(k, prefix); ok {
				di.WANs[w] = c
			}
		}
		di.Top = topServices(labels[d.Key], 3)
		for _, s := range busy[d.Key] {
			di.Now = append(di.Now, *s)
		}
		sort.Slice(di.Now, func(i, j int) bool {
			a, b := di.Now[i], di.Now[j]
			if a.Down+a.Up != b.Down+b.Up {
				return a.Down+a.Up > b.Down+b.Up
			}
			return a.Conns > b.Conns || (a.Conns == b.Conns && a.Name < b.Name)
		})
		if len(di.Now) > 6 {
			di.Now = di.Now[:6]
		}
		out = append(out, di)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Today.Total() != out[j].Today.Total() {
			return out[i].Today.Total() > out[j].Today.Total()
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func topServices(m map[string]store.Counter, n int) []string {
	type kv struct {
		k string
		v uint64
	}
	var l []kv
	for k, c := range m {
		if s, ok := strings.CutPrefix(k, "s:"); ok && s != "" {
			l = append(l, kv{s, c.Total()})
		}
	}
	sort.Slice(l, func(i, j int) bool { return l[i].v > l[j].v })
	var out []string
	for i := 0; i < len(l) && i < n; i++ {
		out = append(out, l[i].k)
	}
	return out
}

func (e *Engine) apiDevices(*ubus.Request) (any, error) {
	e.watched()
	return map[string]any{"now": time.Now().Unix(), "devices": e.devInfos(nil)}, nil
}

type labelRow struct {
	Name     string `json:"name"`
	Category string `json:"category,omitempty"`
	Up       uint64 `json:"up"`
	Down     uint64 `json:"down"`
}

// breakdown splits a group's labels into services, sites, addresses and
// programs, largest first.
func (e *Engine) breakdown(m map[string]store.Counter, limit int) map[string][]labelRow {
	cats := e.categories()
	out := map[string][]labelRow{"services": {}, "sites": {}, "addrs": {}, "procs": {}}
	for k, c := range m {
		kind, name, _ := strings.Cut(k, ":")
		row := labelRow{Name: name, Up: c.Up, Down: c.Down}
		switch kind {
		case "s":
			if name == "" {
				continue
			}
			row.Category = cats[name]
			out["services"] = append(out["services"], row)
		case "h":
			if name == "" {
				row.Name = "(other sites)"
			}
			out["sites"] = append(out["sites"], row)
		case "a":
			out["addrs"] = append(out["addrs"], row)
		case "p":
			out["procs"] = append(out["procs"], row)
		}
	}
	for k, l := range out {
		sort.Slice(l, func(i, j int) bool { return l[i].Up+l[i].Down > l[j].Up+l[j].Down })
		if limit > 0 && len(l) > limit {
			l = l[:limit]
		}
		out[k] = l
	}
	return out
}

// categories maps service names to categories (from rules and live flows).
func (e *Engine) categories() map[string]string {
	m := names.Categories(e.rules.Load())
	e.mu.RLock()
	for _, f := range e.flows {
		if f.Service != "" && m[f.Service] == "" {
			m[f.Service] = f.Category
		}
	}
	e.mu.RUnlock()
	return m
}

func (e *Engine) apiDevice(r *ubus.Request) (any, error) {
	e.watched()
	var q struct {
		Key  string `json:"key"`
		From int64  `json:"from"`
		To   int64  `json:"to"`
	}
	if err := r.Decode(&q); err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	if q.To <= 0 {
		q.To = now + 1
	}
	if q.From <= 0 {
		q.From = e.Store.DayStart(now)
	}
	infos := e.devInfos(map[string]bool{q.Key: true})
	if len(infos) == 0 {
		return nil, &ubus.Error{Status: ubus.StatusNotFound, Msg: "no such device"}
	}
	wans := map[string]store.Counter{}
	for k, c := range e.Store.Totals([]string{"dw:" + q.Key + "|*", "d:" + q.Key, "l:" + q.Key}, q.From, q.To) {
		if w, ok := strings.CutPrefix(k, "dw:"+q.Key+"|"); ok {
			wans[w] = c
		} else {
			wans["("+k[:1]+")"] = c
		}
	}
	labels := e.Store.Labels([]string{q.Key}, q.From, q.To)[q.Key]
	return map[string]any{
		"device": infos[0], "from": q.From, "to": q.To, "wans": wans,
		"breakdown": e.breakdown(labels, 100), "flows": e.flowRows(q.Key, "", 300),
	}, nil
}

func (e *Engine) apiWANs(*ubus.Request) (any, error) {
	e.watched()
	now := time.Now().Unix()
	today := e.Store.DayStart(now)
	cfg := e.Config()
	e.mu.RLock()
	rates := e.rates
	e.mu.RUnlock()
	td := e.Store.Totals([]string{"i:*", "dw:*", "t:*"}, today, now+1)
	labels := e.Store.Labels([]string{"w:*"}, today, now+1)
	var out []map[string]any
	for _, w := range e.Topo().WANs() {
		wc := cfg.WANs[w.Name]
		if wc != nil && wc.Hidden {
			continue
		}
		days := e.Store.History([]string{"i:" + w.Name}, time.Unix(today, 0).In(e.Store.Location()).AddDate(0, 0, -29).Unix(), now+1, 86400)
		var devs []labelRow
		for k, c := range td {
			if rest, ok := strings.CutPrefix(k, "dw:"); ok {
				dev, wan, _ := strings.Cut(rest, "|")
				if wan == w.Name {
					devs = append(devs, labelRow{Name: dev, Up: c.Up, Down: c.Down})
				}
			}
		}
		// tunnels this uplink carried (as transport), and what carried this tunnel
		carries := map[string]store.Counter{}
		carried := map[string]store.Counter{}
		for k, c := range td {
			if rest, ok := strings.CutPrefix(k, "t:"); ok {
				link, tun, _ := strings.Cut(rest, "|")
				if link == w.Name {
					carries[tun] = c
				}
				if tun == w.Name {
					carried[link] = c
				}
			}
		}
		sort.Slice(devs, func(i, j int) bool { return devs[i].Up+devs[i].Down > devs[j].Up+devs[j].Down })
		if len(devs) > 8 {
			devs = devs[:8]
		}
		for i := range devs {
			devs[i].Category = devs[i].Name // key
			devs[i].Name = e.DeviceName(devs[i].Name)
		}
		row := map[string]any{
			"name": w.Name, "label": e.WANLabel(w.Name), "dev": w.Dev, "kind": w.Kind, "up": w.Up,
			"rate": rates["i:"+w.Name], "today": td["i:"+w.Name], "devices": devs,
			"services": e.breakdown(labels["w:"+w.Name], 8)["services"], "days_from": days.From,
			"days": csv(days.Data["i:"+w.Name]), "mwan": w.MwanID, "carries": carries, "carried": carried,
		}
		if w.Gateway.IsValid() {
			row["gateway"] = w.Gateway.String()
		}
		if p := e.planOf(w.Name); p != nil {
			row["plan"] = p.ID
			row["plan_name"] = p.Name
		}
		out = append(out, row)
	}
	return map[string]any{"now": now, "wans": out}, nil
}

type flowRow struct {
	ID       string `json:"id"`
	Dev      string `json:"dev"`
	DevName  string `json:"dev_name"`
	WAN      string `json:"wan,omitempty"`
	Proto    string `json:"proto"`
	Local    string `json:"local"`
	Remote   string `json:"remote"`
	Host     string `json:"host,omitempty"`
	Alias    string `json:"alias,omitempty"` // the CNAME the host resolved through (often its CDN)
	Site     string `json:"site,omitempty"`
	Src      string `json:"src,omitempty"`
	Service  string `json:"service,omitempty"`
	Category string `json:"category,omitempty"`
	Proc     string `json:"proc,omitempty"`
	Tunnel   string `json:"tunnel,omitempty"` // the tunnel this connection carries
	Org      string `json:"org,omitempty"`    // who announces the server's address
	Rule     bool   `json:"rule,omitempty"`   // a rule named its service (else its site, port or owner did)
	First    int64  `json:"first"`
	Last     int64  `json:"last"`
	Up       uint64 `json:"up"`
	Down     uint64 `json:"down"`
	RateUp   uint64 `json:"rate_up"`
	RateDown uint64 `json:"rate_down"`
	Local2   bool   `json:"lan,omitempty"`
}

func protoName(p uint8) string {
	switch p {
	case 6:
		return "tcp"
	case 17:
		return "udp"
	case 1:
		return "icmp"
	case 58:
		return "icmp6"
	}
	return strconv.Itoa(int(p))
}

func (e *Engine) flowRows(dev, wan string, limit int) []flowRow {
	e.mu.RLock()
	var sel []*FlowState
	for _, f := range e.flows {
		if f.Dev == "" {
			continue // broadcasts and the like: nobody's
		}
		if (dev == "" || f.Dev == dev) && (wan == "" || f.WAN == wan) {
			c := *f
			sel = append(sel, &c)
		}
	}
	e.mu.RUnlock()
	sort.Slice(sel, func(i, j int) bool {
		ri, rj := sel[i].RateUp+sel[i].RateDown, sel[j].RateUp+sel[j].RateDown
		if ri != rj {
			return ri > rj
		}
		return sel[i].Up+sel[i].Down > sel[j].Up+sel[j].Down
	})
	if limit > 0 && len(sel) > limit {
		sel = sel[:limit]
	}
	names := map[string]string{}
	out := make([]flowRow, 0, len(sel))
	for _, f := range sel {
		n, ok := names[f.Dev]
		if !ok {
			n = e.DeviceName(f.Dev)
			names[f.Dev] = n
		}
		out = append(out, flowRow{
			ID: fmt.Sprintf("%d", f.Key.ID), Dev: f.Dev, DevName: n, WAN: f.WAN, Proto: protoName(f.Proto),
			Local: f.Local.String(), Remote: f.Remote.String(), Host: f.Host, Alias: f.Alias, Site: f.Site, Src: f.NameSrc,
			Service: f.Service, Category: f.Category, Proc: f.Proc, Tunnel: f.Tunnel, Org: f.Org, Rule: f.Ruled,
			First: f.First, Last: f.Last, Up: f.Up, Down: f.Down,
			RateUp: f.RateUp, RateDown: f.RateDown, Local2: f.LocalOnly,
		})
	}
	return out
}

func (e *Engine) apiFlows(r *ubus.Request) (any, error) {
	e.watched()
	var q struct {
		Device string `json:"device"`
		WAN    string `json:"wan"`
		Limit  int    `json:"limit"`
	}
	if err := r.Decode(&q); err != nil {
		return nil, err
	}
	if q.Limit <= 0 || q.Limit > 2000 {
		q.Limit = 500
	}
	e.mu.RLock()
	total := len(e.flows)
	e.mu.RUnlock()
	return map[string]any{"now": time.Now().Unix(), "total": total, "flows": e.flowRows(q.Device, q.WAN, q.Limit)}, nil
}

func (e *Engine) apiHistory(r *ubus.Request) (any, error) {
	e.watched()
	var q struct {
		Keys []string `json:"keys"`
		From int64    `json:"from"`
		To   int64    `json:"to"`
		Step int64    `json:"step"`
	}
	if err := r.Decode(&q); err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	if q.To <= 0 || q.To > now+1 {
		q.To = now + 1
	}
	if q.From <= 0 || q.From >= q.To {
		q.From = q.To - 86400
	}
	if q.Step == 0 {
		q.Step = 60
	}
	if q.Step > 0 && (q.To-q.From)/q.Step > 3000 {
		return nil, ubus.Errorf("too many points: use a larger step")
	}
	h := e.Store.History(q.Keys, q.From, q.To, q.Step)
	series := map[string]string{}
	for k, d := range h.Data {
		series[k] = csv(d)
	}
	bins := make([]string, len(h.Bins))
	for i, b := range h.Bins {
		bins[i] = strconv.FormatInt(b, 10)
	}
	return map[string]any{"from": h.From, "step": h.Step, "res": h.Label, "bins": strings.Join(bins, ","), "series": series}, nil
}

func (e *Engine) apiTop(r *ubus.Request) (any, error) {
	e.watched()
	var q struct {
		Groups []string `json:"groups"`
		From   int64    `json:"from"`
		To     int64    `json:"to"`
		Limit  int      `json:"limit"`
	}
	if err := r.Decode(&q); err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	if q.To <= 0 {
		q.To = now + 1
	}
	if q.From <= 0 {
		q.From = e.Store.DayStart(now)
	}
	if q.Limit <= 0 {
		q.Limit = 50
	}
	if len(q.Groups) == 0 {
		q.Groups = []string{"*"}
	}
	out := map[string]any{}
	all := map[string]store.Counter{}
	for g, m := range e.Store.Labels(q.Groups, q.From, q.To) {
		out[g] = e.breakdown(m, q.Limit)
		if !strings.HasPrefix(g, "w:") {
			for k, c := range m {
				x := all[k]
				x.Add(c)
				all[k] = x
			}
		}
	}
	return map[string]any{"from": q.From, "to": q.To, "groups": out, "all": e.breakdown(all, q.Limit)}, nil
}

func (e *Engine) apiUsage(r *ubus.Request) (any, error) {
	e.watched()
	var q struct {
		Keys []string `json:"keys"`
		From int64    `json:"from"`
		To   int64    `json:"to"`
	}
	if err := r.Decode(&q); err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	if q.To <= 0 {
		q.To = now + 1
	}
	if len(q.Keys) == 0 {
		q.Keys = []string{"d:*", "i:*"}
	}
	tot := e.Store.Totals(q.Keys, q.From, q.To)
	names := map[string]string{}
	for k := range tot {
		if d, ok := strings.CutPrefix(k, "d:"); ok {
			names[d] = e.DeviceName(d)
		}
	}
	return map[string]any{"from": q.From, "to": q.To, "totals": tot, "names": names}, nil
}

func (e *Engine) apiAlerts(r *ubus.Request) (any, error) {
	var q struct {
		Limit int `json:"limit"`
	}
	r.Decode(&q)
	if q.Limit <= 0 {
		q.Limit = 100
	}
	return map[string]any{"alerts": e.alerts.Log(q.Limit)}, nil
}

func (e *Engine) apiReport(r *ubus.Request) (any, error) {
	var q struct {
		From int64 `json:"from"`
		To   int64 `json:"to"`
	}
	r.Decode(&q)
	now := time.Now().Unix()
	if q.From <= 0 {
		q.From = e.Store.DayStart(now)
	}
	if q.To <= 0 {
		q.To = now + 1
	}
	return map[string]any{"text": e.Report(q.From, q.To)}, nil
}

func (e *Engine) apiNotifyTest(*ubus.Request) (any, error) {
	return map[string]any{"result": e.alerts.Test()}, nil
}

func sectionOf(key string) string {
	return "d_" + strings.NewReplacer(":", "", ".", "_", "-", "_").Replace(strings.TrimPrefix(key, "ip:"))
}

func (e *Engine) apiSetDevice(r *ubus.Request) (any, error) {
	var q struct {
		Key       string    `json:"key"`
		Name      *string   `json:"name"`
		Icon      *string   `json:"icon"`
		Block     *bool     `json:"block"`
		Pause     *int64    `json:"pause"` // minutes; 0 ends a pause, -1 until turned off
		Down      *int      `json:"down"`
		Up        *int      `json:"up"`
		Schedules *[]string `json:"schedules"`
		Quotas    *[]string `json:"quotas"`
		Approved  *bool     `json:"approved"`
	}
	if err := r.Decode(&q); err != nil {
		return nil, err
	}
	if q.Key == "" {
		return nil, ubus.Errorf("pick a device")
	}
	if q.Key == RouterKey && (q.Block != nil || q.Pause != nil || q.Down != nil || q.Up != nil || q.Schedules != nil || q.Quotas != nil || q.Approved != nil) {
		return nil, ubus.Errorf("the router itself can only be renamed")
	}
	if q.Icon != nil {
		ic := strings.TrimSpace(*q.Icon)
		if len(ic) > 40 || strings.ContainsAny(ic, "'\"\\\n") {
			return nil, ubus.Errorf("pick one emoji")
		}
		q.Icon = &ic
	}
	if q.Name != nil && len(*q.Name) > 64 {
		return nil, ubus.Errorf("that name is too long")
	}
	if q.Schedules != nil {
		for _, s := range *q.Schedules {
			if _, err := ParseSchedule(s); err != nil {
				return nil, ubus.Errorf("schedule %q: %v", s, err)
			}
		}
	}
	if q.Quotas != nil {
		for _, s := range *q.Quotas {
			if _, err := ParseQuota(s); err != nil {
				return nil, ubus.Errorf("quota %q: %v", s, err)
			}
		}
	}
	sec := ConfigName + "." + sectionOf(q.Key)
	if dc := e.Config().Devices[q.Key]; dc != nil && dc.Section != "" {
		sec = ConfigName + "." + dc.Section
	} else if dc != nil {
		return nil, ubus.Errorf("this device is set in an unnamed section; edit /etc/config/wanscope")
	}
	cmds := []string{"set " + sec + "=device"}
	if IsMAC(q.Key) {
		cmds = append(cmds, "set "+sec+".mac="+uci.Quote(q.Key))
	} else {
		cmds = append(cmds, "set "+sec+".key="+uci.Quote(q.Key))
	}
	setOpt := func(o, v string) {
		if v == "" || v == "0" {
			cmds = append(cmds, "delete "+sec+"."+o)
		} else {
			cmds = append(cmds, "set "+sec+"."+o+"="+uci.Quote(v))
		}
	}
	if q.Name != nil {
		setOpt("name", strings.TrimSpace(*q.Name))
	}
	if q.Icon != nil {
		setOpt("icon", *q.Icon)
	}
	if q.Block != nil {
		setOpt("block", map[bool]string{true: "1", false: "0"}[*q.Block])
	}
	if q.Pause != nil {
		switch {
		case *q.Pause == 0:
			setOpt("pause_until", "")
		case *q.Pause < 0:
			setOpt("pause_until", "4102444800") // 2100
		default:
			setOpt("pause_until", strconv.FormatInt(time.Now().Unix()+*q.Pause*60, 10))
		}
	}
	if q.Down != nil {
		setOpt("down", strconv.Itoa(max(0, *q.Down)))
	}
	if q.Up != nil {
		setOpt("up", strconv.Itoa(max(0, *q.Up)))
	}
	if q.Schedules != nil {
		cmds = append(cmds, "delete "+sec+".schedule")
		for _, s := range *q.Schedules {
			cmds = append(cmds, "add_list "+sec+".schedule="+uci.Quote(s))
		}
	}
	if q.Quotas != nil {
		cmds = append(cmds, "delete "+sec+".quota")
		for _, s := range *q.Quotas {
			cmds = append(cmds, "add_list "+sec+".quota="+uci.Quote(s))
		}
	}
	if q.Approved != nil {
		setOpt("approved", map[bool]string{true: "1", false: "0"}[*q.Approved])
		if !*q.Approved {
			cmds[len(cmds)-1] = "set " + sec + ".approved='0'"
		}
		e.Devs.Approve(q.Key, *q.Approved)
	}
	if err := uci.Batch(ConfigName, cmds); err != nil {
		return nil, err
	}
	if err := e.Reload(); err != nil {
		return nil, err
	}
	e.policy.tick(time.Now().Unix())
	return map[string]any{"ok": true, "effect": e.policy.Effect(q.Key)}, nil
}

func (e *Engine) apiSetWAN(r *ubus.Request) (any, error) {
	var q struct {
		Name   string  `json:"name"`
		Label  *string `json:"label"`
		Kind   *string `json:"kind"`
		Hidden *bool   `json:"hidden"`
	}
	if err := r.Decode(&q); err != nil {
		return nil, err
	}
	if e.Topo().Iface(q.Name) == nil {
		return nil, ubus.Errorf("no interface %q", q.Name)
	}
	sec := ConfigName + ".wan_" + strings.NewReplacer("-", "_", ".", "_").Replace(q.Name)
	f, _ := uci.Load(ConfigName)
	for _, s := range f.ByType("wan") {
		if s.Get("interface", s.Name) == q.Name && s.Name != "" {
			sec = ConfigName + "." + s.Name
		}
	}
	cmds := []string{"set " + sec + "=wan", "set " + sec + ".interface=" + uci.Quote(q.Name)}
	if q.Label != nil {
		cmds = append(cmds, "set "+sec+".label="+uci.Quote(strings.TrimSpace(*q.Label)))
	}
	if q.Kind != nil {
		cmds = append(cmds, "set "+sec+".kind="+uci.Quote(*q.Kind))
	}
	if q.Hidden != nil {
		cmds = append(cmds, "set "+sec+".hidden="+uci.Quote(map[bool]string{true: "1", false: "0"}[*q.Hidden]))
	}
	if err := uci.Batch(ConfigName, cmds); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, e.Reload()
}

func (e *Engine) apiSetGuest(r *ubus.Request) (any, error) {
	var q struct {
		Policy string `json:"policy"`
		Down   int    `json:"down"`
		Up     int    `json:"up"`
	}
	if err := r.Decode(&q); err != nil {
		return nil, err
	}
	switch q.Policy {
	case "allow", "limit", "block":
	default:
		return nil, ubus.Errorf("policy is allow, limit or block")
	}
	cmds := []string{"set wanscope.guest=guest", "set wanscope.guest.policy=" + uci.Quote(q.Policy),
		"set wanscope.guest.down=" + uci.Quote(strconv.Itoa(q.Down)), "set wanscope.guest.up=" + uci.Quote(strconv.Itoa(q.Up))}
	if e.Config().Guest.Policy == "allow" && q.Policy != "allow" {
		// everyone already here is trusted
		cmds = append(cmds, "set wanscope.guest.since="+uci.Quote(strconv.FormatInt(time.Now().Unix(), 10)))
		for _, d := range e.Devs.All() {
			e.Devs.Approve(d.Key, true)
		}
	}
	if err := uci.Batch(ConfigName, cmds); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, e.Reload()
}

func (e *Engine) apiForget(r *ubus.Request) (any, error) {
	var q struct {
		Key string `json:"key"`
	}
	if err := r.Decode(&q); err != nil {
		return nil, err
	}
	if dc := e.Config().Devices[q.Key]; dc != nil && dc.Section != "" {
		if err := uci.Batch(ConfigName, []string{"delete " + ConfigName + "." + dc.Section}); err != nil {
			return nil, err
		}
	}
	e.Devs.Forget(q.Key)
	e.Devs.Save(true)
	return map[string]any{"ok": true}, e.Reload()
}
