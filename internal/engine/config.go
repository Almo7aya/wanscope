package engine

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Almo7aya/wanscope/internal/names"
	"github.com/Almo7aya/wanscope/internal/uci"
)

const ConfigName = "wanscope"

type Config struct {
	Enabled     bool
	DataDir     string
	Interval    time.Duration
	IdlePoll    int64 // seconds between conntrack reads when no page is open
	LiveSeconds int
	MinuteDays  int
	HourDays    int
	MemDays     int
	Sniff       bool
	SniffDevs   []string
	LAN         []string // logical LAN interfaces (empty: the firewall's lan zone)
	OfflineSecs int64

	UpdateCheck bool   // look for new releases once a day (update.go)
	UpdateAuto  bool   // and install them at night
	UpdateRepo  string // owner/name on GitHub

	WANs    map[string]*WANConf
	Plans   []*PlanConf
	Devices map[string]*DeviceConf // by lower-case MAC (or "ip:..." key)
	Guest   GuestConf
	Notify  NotifyConf
	Rules   []names.CustomRule

	// BlockServices are blocked on every device; a device's own are in its DeviceConf (svcblock.go)
	BlockServices []string
	blockAll      map[string]bool
	blockDev      map[string]map[string]bool
}

// ServiceBlocked tells whether svc is blocked on device dev (on it alone, or on every device).
func (c *Config) ServiceBlocked(dev, svc string) bool {
	return c.blockAll[svc] || c.blockDev[dev][svc]
}

// AnyServiceBlocked tells whether a service is blocked anywhere.
func (c *Config) AnyServiceBlocked() bool { return len(c.blockAll) > 0 || len(c.blockDev) > 0 }

type WANConf struct {
	Name   string
	Label  string
	Kind   string // auto | uplink | tunnel
	Hidden bool
}

// PlanConf is a data plan: a bundle of data that one or more uplinks draw
// from (SIMs on the same modem share one).
type PlanConf struct {
	ID        string // UCI section name
	Name      string
	Ifaces    []string
	Type      string // prepaid: bought, used up, bought again | monthly: renews on a day each month
	SizeGB    float64
	Start     int64   // prepaid: when the current bundle was bought
	AdjustMB  float64 // correction so the count matches the carrier's
	CycleDay  int     // monthly
	ValidDays int     // prepaid: the bundle expires after this many days (0 = never)
}

type DeviceConf struct {
	Section    string
	Key        string
	Name       string
	Icon       string
	Block      bool
	PauseUntil int64
	Down, Up   int // limits in kB/s (1000 bytes per second), 0 = none
	Schedules  []Schedule
	Quotas     []Quota
	Approved   bool
	// BlockServices are services whose addresses this device can't reach (svcblock.go)
	BlockServices []string
}

// Schedule applies an action on some days between two times of day
// ("mon,tue 23:00-07:00 block", "weekend 08:00-12:00 limit 2000 500").
type Schedule struct {
	Days       [7]bool // Sunday = 0
	Start, End int     // minutes after midnight; End < Start wraps past midnight
	Action     string  // block | limit
	Down, Up   int
	Raw        string
}

// Quota caps a device's traffic per period ("day total 2048 block",
// "month down 10240 limit 1000 250", "week total 5000 alert").
type Quota struct {
	Period   string // day | week | month
	Dir      string // total | down | up
	MB       float64
	Action   string // block | limit | alert
	Down, Up int
	Raw      string
}

type GuestConf struct {
	Policy   string // allow | limit | block (devices first seen after it was set)
	Down, Up int
	Since    int64
}

type NotifyConf struct {
	Method        string // none | telegram | command
	Token, Chat   string
	Command       string
	NewDevice     bool
	CapPercents   []int
	DailyReport   bool
	ReportTime    string // "09:00"
	BurstMB       float64
	BurstMinutes  int
	QuotaAlerts   bool
	OfflineAlerts bool
	Updates       bool // a new wanscope release
}

func atoi(s string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return v
	}
	return def
}

func atof(s string, def float64) float64 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
		return v
	}
	return def
}

func LoadConfig() (*Config, error) {
	f, err := uci.Load(ConfigName)
	if err != nil {
		return nil, err
	}
	c := &Config{
		Enabled: true, DataDir: "/usr/share/wanscope", Interval: time.Second, IdlePoll: 5, LiveSeconds: 3600,
		MinuteDays: 365, HourDays: 731, MemDays: 35, Sniff: true, OfflineSecs: 180, UpdateCheck: true, UpdateRepo: defaultRepo,
		WANs: map[string]*WANConf{}, Devices: map[string]*DeviceConf{},
		Guest:  GuestConf{Policy: "allow"},
		Notify: NotifyConf{Method: "none", NewDevice: true, Updates: true, CapPercents: []int{80, 100}, ReportTime: "09:00", BurstMinutes: 10, QuotaAlerts: true},
	}
	if m := f.ByName("main"); m != nil {
		c.Enabled = m.Bool("enabled", true)
		c.DataDir = m.Get("data_dir", c.DataDir)
		c.Interval = time.Duration(max(1, min(5, atoi(m.Get("interval", "1"), 1)))) * time.Second
		c.IdlePoll = int64(max(1, min(30, atoi(m.Get("idle_interval", "5"), 5))))
		c.LiveSeconds = max(60, min(86400, atoi(m.Get("live_seconds", "3600"), 3600)))
		c.MinuteDays = max(1, atoi(m.Get("minute_days", "365"), 365))
		c.HourDays = max(7, atoi(m.Get("hour_days", "731"), 731))
		c.MemDays = max(2, min(c.HourDays, atoi(m.Get("memory_days", "35"), 35)))
		c.Sniff = m.Bool("sniff", true)
		c.SniffDevs = m.List("sniff_device")
		c.LAN = m.List("lan")
		c.OfflineSecs = int64(max(30, atoi(m.Get("offline_timeout", "180"), 180)))
		c.BlockServices = m.List("block_service")
		c.UpdateCheck = m.Bool("update_check", true)
		c.UpdateAuto = m.Bool("update_auto", false)
		c.UpdateRepo = m.Get("update_repo", defaultRepo)
	}
	for _, s := range f.ByType("wan") {
		name := s.Get("interface", s.Name)
		if name == "" {
			continue
		}
		c.WANs[name] = &WANConf{Name: name, Label: s.Get("label", ""), Kind: s.Get("kind", "auto"), Hidden: s.Bool("hidden", false)}
	}
	for _, s := range f.ByType("plan") {
		if s.Name == "" {
			continue
		}
		p := &PlanConf{ID: s.Name, Name: s.Get("name", s.Name), Ifaces: s.List("iface"), Type: s.Get("type", "prepaid"),
			SizeGB: atof(s.Get("size_gb", "0"), 0), Start: int64(atof(s.Get("start", "0"), 0)), AdjustMB: atof(s.Get("adjust_mb", "0"), 0),
			CycleDay: max(1, min(28, atoi(s.Get("cycle_day", "1"), 1))), ValidDays: max(0, atoi(s.Get("valid_days", "0"), 0))}
		if p.Type != "monthly" {
			p.Type = "prepaid"
		}
		c.Plans = append(c.Plans, p)
	}
	for _, s := range f.ByType("device") {
		key := strings.ToLower(s.Get("mac", ""))
		if key == "" {
			key = s.Get("key", "")
		}
		if key == "" {
			continue
		}
		d := &DeviceConf{
			Section: s.Name, Key: key, Name: s.Get("name", ""), Icon: s.Get("icon", ""),
			Block: s.Bool("block", false), PauseUntil: int64(atoi(s.Get("pause_until", "0"), 0)),
			Down: atoi(s.Get("down", "0"), 0), Up: atoi(s.Get("up", "0"), 0), Approved: s.Bool("approved", true),
			BlockServices: s.List("block_service"),
		}
		for _, r := range s.List("schedule") {
			if sc, err := ParseSchedule(r); err == nil {
				d.Schedules = append(d.Schedules, sc)
			}
		}
		for _, r := range s.List("quota") {
			if q, err := ParseQuota(r); err == nil {
				d.Quotas = append(d.Quotas, q)
			}
		}
		c.Devices[key] = d
	}
	if g := f.ByName("guest"); g != nil {
		c.Guest = GuestConf{Policy: g.Get("policy", "allow"), Down: atoi(g.Get("down", "0"), 0), Up: atoi(g.Get("up", "0"), 0), Since: int64(atoi(g.Get("since", "0"), 0))}
	}
	if n := f.ByName("notify"); n != nil {
		c.Notify.Method = n.Get("method", "none")
		c.Notify.Token = n.Get("telegram_token", "")
		c.Notify.Chat = n.Get("telegram_chat", "")
		c.Notify.Command = n.Get("command", "")
		c.Notify.NewDevice = n.Bool("new_device", true)
		c.Notify.QuotaAlerts = n.Bool("quota", true)
		c.Notify.OfflineAlerts = n.Bool("wan_offline", false)
		c.Notify.Updates = n.Bool("update", true)
		if l := n.List("cap_percent"); len(l) > 0 {
			c.Notify.CapPercents = nil
			for _, p := range l {
				if v := atoi(p, 0); v > 0 {
					c.Notify.CapPercents = append(c.Notify.CapPercents, v)
				}
			}
		}
		c.Notify.DailyReport = n.Bool("daily_report", false)
		c.Notify.ReportTime = n.Get("report_time", "09:00")
		c.Notify.BurstMB = atof(n.Get("burst_mb", "0"), 0)
		c.Notify.BurstMinutes = max(1, atoi(n.Get("burst_minutes", "10"), 10))
	}
	for _, s := range f.ByType("rule") {
		r := names.CustomRule{Section: s.Name, Name: s.Get("name", ""), Category: s.Get("category", "other"),
			Domains: s.List("domain"), CIDRs: s.List("cidr"), Ports: s.List("port")}
		if r.Name != "" {
			c.Rules = append(c.Rules, r)
		}
	}
	c.blockAll, c.blockDev = map[string]bool{}, map[string]map[string]bool{}
	for _, s := range c.BlockServices {
		c.blockAll[s] = true
	}
	for k, d := range c.Devices {
		for _, s := range d.BlockServices {
			if c.blockDev[k] == nil {
				c.blockDev[k] = map[string]bool{}
			}
			c.blockDev[k][s] = true
		}
	}
	return c, nil
}

var dayNames = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

func parseClock(s string) (int, error) {
	h, m, ok := strings.Cut(s, ":")
	if !ok {
		return 0, fmt.Errorf("bad time %q", s)
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || hh < 0 || hh > 24 || mm < 0 || mm > 59 {
		return 0, fmt.Errorf("bad time %q", s)
	}
	return hh*60 + mm, nil
}

func ParseSchedule(s string) (Schedule, error) {
	f := strings.Fields(strings.ToLower(s))
	sc := Schedule{Raw: s}
	if len(f) < 3 {
		return sc, fmt.Errorf("schedule needs days, times and an action")
	}
	for _, d := range strings.Split(f[0], ",") {
		switch d {
		case "daily", "all":
			for i := range sc.Days {
				sc.Days[i] = true
			}
		case "weekdays":
			for i := 1; i <= 5; i++ {
				sc.Days[i] = true
			}
		case "weekend":
			sc.Days[0], sc.Days[6] = true, true
		default:
			ok := false
			for i, n := range dayNames {
				if d == n {
					sc.Days[i], ok = true, true
				}
			}
			if !ok {
				return sc, fmt.Errorf("bad day %q", d)
			}
		}
	}
	a, b, ok := strings.Cut(f[1], "-")
	if !ok {
		return sc, fmt.Errorf("bad time range %q", f[1])
	}
	var err error
	if sc.Start, err = parseClock(a); err != nil {
		return sc, err
	}
	if sc.End, err = parseClock(b); err != nil {
		return sc, err
	}
	sc.Action = f[2]
	switch sc.Action {
	case "block":
	case "limit":
		if len(f) < 4 {
			return sc, fmt.Errorf("limit needs a download rate")
		}
		sc.Down = atoi(f[3], 0)
		if len(f) > 4 {
			sc.Up = atoi(f[4], 0)
		}
	default:
		return sc, fmt.Errorf("bad action %q", sc.Action)
	}
	return sc, nil
}

// Active reports whether the schedule covers local time t.
func (sc Schedule) Active(t time.Time) bool {
	m := t.Hour()*60 + t.Minute()
	wd := int(t.Weekday())
	if sc.Start <= sc.End {
		return sc.Days[wd] && m >= sc.Start && m < sc.End
	}
	// wraps midnight: the evening part belongs to the start day
	if m >= sc.Start {
		return sc.Days[wd]
	}
	return m < sc.End && sc.Days[(wd+6)%7]
}

func ParseQuota(s string) (Quota, error) {
	f := strings.Fields(strings.ToLower(s))
	q := Quota{Raw: s}
	if len(f) < 4 {
		return q, fmt.Errorf("quota needs period, direction, MB and action")
	}
	q.Period, q.Dir = f[0], f[1]
	switch q.Period {
	case "day", "week", "month":
	default:
		return q, fmt.Errorf("bad period %q", q.Period)
	}
	switch q.Dir {
	case "total", "down", "up":
	default:
		return q, fmt.Errorf("bad direction %q", q.Dir)
	}
	q.MB = atof(f[2], 0)
	if q.MB <= 0 {
		return q, fmt.Errorf("bad size %q", f[2])
	}
	q.Action = f[3]
	switch q.Action {
	case "block", "alert":
	case "limit":
		if len(f) < 5 {
			return q, fmt.Errorf("limit needs a download rate")
		}
		q.Down = atoi(f[4], 0)
		if len(f) > 5 {
			q.Up = atoi(f[5], 0)
		}
	default:
		return q, fmt.Errorf("bad action %q", q.Action)
	}
	return q, nil
}
