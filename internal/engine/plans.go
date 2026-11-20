package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Almo7aya/wanscope/internal/store"
	"github.com/Almo7aya/wanscope/internal/ubus"
	"github.com/Almo7aya/wanscope/internal/uci"
)

// A data plan counts the traffic of its uplinks on the links themselves (IP
// bytes, as carriers count) since the bundle was bought (prepaid) or since
// the cycle began (monthly).

// Purchase is one bundle bought for a plan.
type Purchase struct {
	T      int64   `json:"t"`
	SizeGB float64 `json:"size_gb"` // what was bought
	Mode   string  `json:"mode"`    // fresh | add (on top of what was left)
	Used   uint64  `json:"used"`    // the previous bundle's use when this one started
	Left   int64   `json:"left"`    // what was left of it
	Lasted int64   `json:"lasted"`  // seconds the previous bundle lasted
}

type PlanStatus struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	Type      string        `json:"type"`
	Ifaces    []string      `json:"ifaces"`
	Size      uint64        `json:"size"`
	Start     int64         `json:"start"`
	End       int64         `json:"end,omitempty"` // cycle end, or when the bundle expires
	CycleDay  int           `json:"cycle_day,omitempty"`
	ValidDays int           `json:"valid_days,omitempty"`
	Measured  store.Counter `json:"measured"`
	Adjust    int64         `json:"adjust"`
	Used      uint64        `json:"used"` // measured plus the correction
	Left      int64         `json:"left"`
	Pace      float64       `json:"pace"`               // bytes a day lately
	RunsOut   int64         `json:"runs_out,omitempty"` // estimate
	Today     store.Counter `json:"today"`
	DaysFrom  int64         `json:"days_from"`
	Days      string        `json:"days"` // per-day use since the start (csv down,up)
	History   []Purchase    `json:"history"`
}

type purchases struct {
	mu   sync.Mutex
	path string
	m    map[string][]Purchase
}

func loadPurchases(dir string) *purchases {
	p := &purchases{path: filepath.Join(dir, "purchases.json"), m: map[string][]Purchase{}}
	if raw, err := os.ReadFile(p.path); err == nil {
		json.Unmarshal(raw, &p.m)
	}
	return p
}

func (p *purchases) add(id string, pu Purchase) {
	p.mu.Lock()
	p.m[id] = append(p.m[id], pu)
	if len(p.m[id]) > 100 {
		p.m[id] = p.m[id][len(p.m[id])-100:]
	}
	raw, _ := json.Marshal(p.m)
	p.mu.Unlock()
	os.WriteFile(p.path+".tmp", raw, 0o644)
	os.Rename(p.path+".tmp", p.path)
}

func (p *purchases) list(id string) []Purchase {
	p.mu.Lock()
	defer p.mu.Unlock()
	l := p.m[id]
	out := make([]Purchase, len(l))
	for i := range l {
		out[i] = l[len(l)-1-i] // newest first
	}
	return out
}

func (e *Engine) planWindow(p *PlanConf, now int64) (start, end int64) {
	loc := e.Store.Location()
	if p.Type == "monthly" {
		lt := time.Unix(now, 0).In(loc)
		s := time.Date(lt.Year(), lt.Month(), p.CycleDay, 0, 0, 0, 0, loc)
		if s.After(lt) {
			s = s.AddDate(0, -1, 0)
		}
		return s.Unix(), s.AddDate(0, 1, 0).Unix()
	}
	start = p.Start
	if start <= 0 || start > now {
		start = now
	}
	if p.ValidDays > 0 {
		end = start + int64(p.ValidDays)*86400
	}
	return start, end
}

func ifaceKeys(ifaces []string) []string {
	keys := make([]string, len(ifaces))
	for i, n := range ifaces {
		keys[i] = "i:" + n
	}
	return keys
}

func sumCounters(m map[string]store.Counter) store.Counter {
	var c store.Counter
	for _, x := range m {
		c.Add(x)
	}
	return c
}

// PlanStatus is what a plan used and has left.
func (e *Engine) PlanStatus(p *PlanConf, now int64, withDays bool) PlanStatus {
	start, end := e.planWindow(p, now)
	keys := ifaceKeys(p.Ifaces)
	st := PlanStatus{ID: p.ID, Name: p.Name, Type: p.Type, Ifaces: p.Ifaces, Size: uint64(p.SizeGB * 1e9),
		Start: start, End: end, Adjust: int64(p.AdjustMB * 1e6), ValidDays: p.ValidDays, History: e.buys.list(p.ID)}
	if p.Type == "monthly" {
		st.CycleDay = p.CycleDay
	}
	st.Measured = sumCounters(e.Store.Totals(keys, start, now+1))
	used := int64(st.Measured.Total()) + st.Adjust
	if used < 0 {
		used = 0
	}
	st.Used = uint64(used)
	st.Left = int64(st.Size) - used
	st.Today = sumCounters(e.Store.Totals(keys, e.Store.DayStart(now), now+1))
	// pace: the last 7 days, or since the start when that is shorter
	from := max(start, now-7*86400)
	if span := now - from; span >= 3600 {
		recent := sumCounters(e.Store.Totals(keys, from, now+1)).Total()
		st.Pace = float64(recent) / float64(span) * 86400
		if st.Pace > 0 && st.Left > 0 {
			st.RunsOut = now + int64(float64(st.Left)/st.Pace*86400)
		}
	}
	if withDays {
		dfrom := max(start, now-62*86400)
		h := e.Store.History(keys, dfrom, now+1, 86400)
		sum := make([]store.Counter, len(h.Bins))
		for _, d := range h.Data {
			for i, c := range d {
				sum[i].Add(c)
			}
		}
		if len(sum) > 0 {
			// the first day counts only from the purchase
			end := now + 1
			if len(h.Bins) > 1 {
				end = h.Bins[1]
			}
			sum[0] = sumCounters(e.Store.Totals(keys, dfrom, end))
		}
		st.DaysFrom, st.Days = h.From, csv(sum)
	}
	return st
}

// planOf names the plan an uplink draws from.
func (e *Engine) planOf(iface string) *PlanConf {
	for _, p := range e.Config().Plans {
		for _, i := range p.Ifaces {
			if i == iface {
				return p
			}
		}
	}
	return nil
}

func (e *Engine) apiPlans(*ubus.Request) (any, error) {
	e.watched()
	now := time.Now().Unix()
	out := []PlanStatus{}
	for _, p := range e.Config().Plans {
		out = append(out, e.PlanStatus(p, now, true))
	}
	return map[string]any{"now": now, "plans": out}, nil
}

var planIDRe = regexp.MustCompile(`[^a-z0-9_]`)

func (e *Engine) apiSetPlan(r *ubus.Request) (any, error) {
	var q struct {
		ID        string   `json:"id"`
		Name      string   `json:"name"`
		Type      string   `json:"type"`
		SizeGB    string   `json:"size_gb"`
		Ifaces    []string `json:"ifaces"`
		CycleDay  int      `json:"cycle_day"`
		ValidDays int      `json:"valid_days"`
		UsedGB    string   `json:"used_gb"` // a new plan: already used of the current bundle
	}
	if err := r.Decode(&q); err != nil {
		return nil, err
	}
	q.Name = strings.TrimSpace(q.Name)
	if q.Name == "" || len(q.Name) > 64 {
		return nil, ubus.Errorf("give the plan a name")
	}
	size := atof(q.SizeGB, -1)
	if size <= 0 {
		return nil, ubus.Errorf("the size is a number of GB")
	}
	if len(q.Ifaces) == 0 {
		return nil, ubus.Errorf("pick the uplinks that use this plan")
	}
	topo := e.Topo()
	for _, i := range q.Ifaces {
		if topo.Iface(i) == nil {
			return nil, ubus.Errorf("no interface %q", i)
		}
	}
	if q.Type != "monthly" {
		q.Type = "prepaid"
	}
	if q.Type == "monthly" && (q.CycleDay < 1 || q.CycleDay > 28) {
		return nil, ubus.Errorf("the cycle starts on a day from 1 to 28")
	}
	cfg := e.Config()
	var old *PlanConf
	for _, p := range cfg.Plans {
		if p.ID == q.ID {
			old = p
		}
	}
	id := q.ID
	if old == nil {
		id = "plan_" + planIDRe.ReplaceAllString(strings.ToLower(q.Name), "_")
		for n := 2; ; n++ {
			taken := false
			for _, p := range cfg.Plans {
				taken = taken || p.ID == id
			}
			if !taken {
				break
			}
			id = fmt.Sprintf("plan_%s_%d", planIDRe.ReplaceAllString(strings.ToLower(q.Name), "_"), n)
		}
	}
	// an uplink draws from one plan
	var cmds []string
	for _, p := range cfg.Plans {
		if p.ID == id {
			continue
		}
		for _, i := range p.Ifaces {
			for _, j := range q.Ifaces {
				if i == j {
					cmds = append(cmds, "del_list "+ConfigName+"."+p.ID+".iface="+uci.Quote(i))
				}
			}
		}
	}
	sec := ConfigName + "." + id
	cmds = append(cmds, "set "+sec+"=plan", "set "+sec+".name="+uci.Quote(q.Name), "set "+sec+".type="+uci.Quote(q.Type),
		"set "+sec+".size_gb="+uci.Quote(strconv.FormatFloat(size, 'f', -1, 64)), "delete "+sec+".iface")
	for _, i := range q.Ifaces {
		cmds = append(cmds, "add_list "+sec+".iface="+uci.Quote(i))
	}
	if q.Type == "monthly" {
		cmds = append(cmds, "set "+sec+".cycle_day="+uci.Quote(strconv.Itoa(q.CycleDay)))
	} else {
		cmds = append(cmds, "set "+sec+".valid_days="+uci.Quote(strconv.Itoa(max(0, q.ValidDays))))
	}
	now := time.Now().Unix()
	if old == nil {
		cmds = append(cmds, "set "+sec+".start="+uci.Quote(strconv.FormatInt(now, 10)))
		if u := atof(q.UsedGB, 0); u > 0 {
			cmds = append(cmds, "set "+sec+".adjust_mb="+uci.Quote(strconv.FormatFloat(u*1000, 'f', 0, 64)))
		}
	}
	if err := uci.Batch(ConfigName, cmds); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "id": id}, e.Reload()
}

func (e *Engine) findPlan(id string) (*PlanConf, error) {
	for _, p := range e.Config().Plans {
		if p.ID == id {
			return p, nil
		}
	}
	return nil, ubus.Errorf("no such plan")
}

// apiTopup records a bundle bought: the count starts again from now.
func (e *Engine) apiTopup(r *ubus.Request) (any, error) {
	var q struct {
		ID     string `json:"id"`
		SizeGB string `json:"size_gb"`
		Mode   string `json:"mode"` // fresh | add
	}
	if err := r.Decode(&q); err != nil {
		return nil, err
	}
	p, err := e.findPlan(q.ID)
	if err != nil {
		return nil, err
	}
	bought := atof(q.SizeGB, -1)
	if bought <= 0 {
		return nil, ubus.Errorf("the size is a number of GB")
	}
	now := time.Now().Unix()
	st := e.PlanStatus(p, now, false)
	size := bought
	if q.Mode == "add" && st.Left > 0 {
		size += float64(st.Left) / 1e9
	} else {
		q.Mode = "fresh"
	}
	sec := ConfigName + "." + p.ID
	if err := uci.Batch(ConfigName, []string{"set " + sec + ".type='prepaid'", "set " + sec + ".start=" + uci.Quote(strconv.FormatInt(now, 10)),
		"set " + sec + ".size_gb=" + uci.Quote(strconv.FormatFloat(size, 'f', 3, 64)), "delete " + sec + ".adjust_mb"}); err != nil {
		return nil, err
	}
	e.buys.add(p.ID, Purchase{T: now, SizeGB: bought, Mode: q.Mode, Used: st.Used, Left: st.Left, Lasted: now - st.Start})
	e.alerts.emit("topup", p.ID, fmt.Sprintf("💳 %s: bought %s%s. The count starts again now.", p.Name,
		HumanBytes(uint64(bought*1e9)), map[bool]string{true: fmt.Sprintf(" on top of the %s left", HumanBytes(uint64(max(0, st.Left)))), false: ""}[q.Mode == "add"]), false)
	return map[string]any{"ok": true}, e.Reload()
}

// apiCalibrate makes the count match what the carrier says is left.
func (e *Engine) apiCalibrate(r *ubus.Request) (any, error) {
	var q struct {
		ID     string `json:"id"`
		LeftGB string `json:"left_gb"`
	}
	if err := r.Decode(&q); err != nil {
		return nil, err
	}
	p, err := e.findPlan(q.ID)
	if err != nil {
		return nil, err
	}
	left := atof(q.LeftGB, -1)
	if left < 0 || left > p.SizeGB {
		return nil, ubus.Errorf("what is left is a number of GB up to the plan's %g GB", p.SizeGB)
	}
	st := e.PlanStatus(p, time.Now().Unix(), false)
	adjustMB := ((p.SizeGB-left)*1e9 - float64(st.Measured.Total())) / 1e6
	if err := uci.Batch(ConfigName, []string{"set " + ConfigName + "." + p.ID + ".adjust_mb=" + uci.Quote(strconv.FormatFloat(adjustMB, 'f', 0, 64))}); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, e.Reload()
}

func (e *Engine) apiDeletePlan(r *ubus.Request) (any, error) {
	var q struct {
		ID string `json:"id"`
	}
	if err := r.Decode(&q); err != nil {
		return nil, err
	}
	if _, err := e.findPlan(q.ID); err != nil {
		return nil, err
	}
	if err := uci.Batch(ConfigName, []string{"delete " + ConfigName + "." + q.ID}); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, e.Reload()
}
