// Package store keeps traffic counters at four resolutions: every second for
// the live view (memory only), per minute, per hour (with the per-device
// breakdown by service, site and program) and per day, the last three in
// append-only files.
package store

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Counter struct {
	Up   uint64 `json:"up"`
	Down uint64 `json:"down"`
}

func (c *Counter) Add(o Counter) { c.Up += o.Up; c.Down += o.Down }
func (c Counter) Total() uint64  { return c.Up + c.Down }

// Bucket is the traffic of one interval. Series are totals by key ("d:<dev>",
// "i:<iface>", ...). Labels break groups (devices, interfaces) down by
// label ("s:<service>", "h:<site>", "p:<program>").
type Bucket struct {
	T      int64
	Series map[string]Counter
	Labels map[string]map[string]Counter
}

func newBucket(t int64) *Bucket {
	return &Bucket{T: t, Series: map[string]Counter{}, Labels: map[string]map[string]Counter{}}
}

func (b *Bucket) merge(o *Bucket) {
	for k, c := range o.Series {
		x := b.Series[k]
		x.Add(c)
		b.Series[k] = x
	}
	for g, m := range o.Labels {
		dst := b.Labels[g]
		if dst == nil {
			dst = make(map[string]Counter, len(m))
			b.Labels[g] = dst
		}
		for l, c := range m {
			x := dst[l]
			x.Add(c)
			dst[l] = x
		}
	}
}

var (
	internMu sync.Mutex
	interned = map[string]string{}
)

func intern(s string) string {
	internMu.Lock()
	defer internMu.Unlock()
	if v, ok := interned[s]; ok {
		return v
	}
	if len(interned) > 200000 {
		interned = map[string]string{}
	}
	interned[s] = s
	return s
}

type Options struct {
	Dir         string
	LiveSeconds int // 1-second history kept in memory
	MinuteDays  int // minute files kept
	HourDays    int // hour files kept
	MemDays     int // hours kept in memory (quotas and caps read them)
	FlushEvery  time.Duration
	TopLabels   int // per group per hour
}

type Store struct {
	opt Options
	loc *time.Location
	log *slog.Logger

	mu       sync.RWMutex
	live     map[string]*ring
	liveEnd  int64
	minutes  []*Bucket // last 48 h
	hours    []*Bucket // last MemDays (series and labels)
	days     []*Bucket // all days, series only
	curMin   *Bucket
	curHour  *Bucket
	pendMin  []*Bucket // finished minutes not yet written
	minFrom  int64     // minutes in memory are complete from here
	hourFrom int64     // hours in memory are complete from here
	lastSave time.Time
	writers  map[string]*fileWriter
}

type ring struct {
	up, down []uint32
	last     int64 // last second with traffic
}

func Open(opt Options, loc *time.Location, log *slog.Logger) (*Store, error) {
	if opt.LiveSeconds <= 0 {
		opt.LiveSeconds = 3600
	}
	if opt.MinuteDays <= 0 {
		opt.MinuteDays = 365
	}
	if opt.HourDays <= 0 {
		opt.HourDays = 731
	}
	if opt.MemDays <= 0 {
		opt.MemDays = 35
	}
	if opt.FlushEvery <= 0 {
		opt.FlushEvery = time.Minute
	}
	if opt.TopLabels <= 0 {
		opt.TopLabels = 60
	}
	if err := os.MkdirAll(opt.Dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{opt: opt, loc: loc, log: log, live: map[string]*ring{}, writers: map[string]*fileWriter{}, lastSave: time.Now()}
	if err := s.load(time.Now()); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Location() *time.Location { return s.loc }

// DayStart is the local midnight starting t's day.
func (s *Store) DayStart(t int64) int64 {
	lt := time.Unix(t, 0).In(s.loc)
	return time.Date(lt.Year(), lt.Month(), lt.Day(), 0, 0, 0, 0, s.loc).Unix()
}

func (s *Store) hourStart(t int64) int64 {
	lt := time.Unix(t, 0).In(s.loc)
	return time.Date(lt.Year(), lt.Month(), lt.Day(), lt.Hour(), 0, 0, 0, s.loc).Unix()
}

func (s *Store) path(kind string, t int64) string {
	lt := time.Unix(t, 0).In(s.loc)
	switch kind {
	case "min":
		return filepath.Join(s.opt.Dir, "min", lt.Format("20060102")+".wsb")
	case "hour":
		return filepath.Join(s.opt.Dir, "hour", lt.Format("200601")+".wsb")
	}
	return filepath.Join(s.opt.Dir, "day", lt.Format("2006")+".wsb")
}

func (s *Store) load(now time.Time) error {
	t := now.Unix()
	s.minFrom = t - 2*86400
	s.hourFrom = t - int64(s.opt.MemDays)*86400
	// minutes of today and yesterday
	for _, d := range []int64{t - 86400, t} {
		bs, err := readBuckets(s.path("min", d))
		if err != nil {
			return err
		}
		for _, b := range bs {
			if b.T >= t-2*86400 {
				b.Labels = nil
				s.minutes = append(s.minutes, b)
			}
		}
	}
	// hours of the last MemDays: one file per month
	from := t - int64(s.opt.MemDays)*86400
	seen := map[string]bool{}
	for d := from; d <= t+86400; d += 86400 {
		p := s.path("hour", d)
		if seen[p] {
			continue
		}
		seen[p] = true
		bs, err := readBuckets(p)
		if err != nil {
			return err
		}
		for _, b := range bs {
			if b.T >= from {
				s.hours = append(s.hours, b)
			}
		}
	}
	// every day's totals
	entries, _ := os.ReadDir(filepath.Join(s.opt.Dir, "day"))
	for _, e := range entries {
		bs, err := readBuckets(filepath.Join(s.opt.Dir, "day", e.Name()))
		if err != nil {
			return err
		}
		for _, b := range bs {
			b.Labels = nil
			s.days = append(s.days, b)
		}
	}
	byT := func(l []*Bucket) { sort.SliceStable(l, func(i, j int) bool { return l[i].T < l[j].T }) }
	byT(s.minutes)
	byT(s.hours)
	byT(s.days)
	s.catchUpDays(t)
	return nil
}

// catchUpDays writes day buckets for finished days that have hours but no
// day bucket yet (the daemon was off at midnight).
func (s *Store) catchUpDays(now int64) {
	last := int64(-1)
	if n := len(s.days); n > 0 {
		last = s.days[n-1].T
	}
	today := s.DayStart(now)
	var day *Bucket
	flush := func() {
		if day != nil && day.T > last && day.T < today {
			s.writeDay(day)
		}
	}
	for _, h := range s.hours {
		d := s.DayStart(h.T)
		if day == nil || day.T != d {
			flush()
			day = newBucket(d)
		}
		day.merge(h)
	}
	flush()
}

func (s *Store) writeDay(day *Bucket) {
	day.Labels = topLabels(day.Labels, s.opt.TopLabels)
	if err := s.writeTo("day", day); err != nil {
		s.log.Warn("store: write day", "err", err)
	}
	s.days = append(s.days, &Bucket{T: day.T, Series: day.Series})
}

func (s *Store) writeTo(kind string, bs ...*Bucket) error {
	if len(bs) == 0 {
		return nil
	}
	byPath := map[string][]*Bucket{}
	var order []string
	for _, b := range bs {
		p := s.path(kind, b.T)
		if _, ok := byPath[p]; !ok {
			order = append(order, p)
		}
		byPath[p] = append(byPath[p], b)
	}
	for _, p := range order {
		w := s.writers[p]
		if w == nil {
			var err error
			if w, err = openWriter(p); err != nil {
				return err
			}
			// one open file per kind
			for k, o := range s.writers {
				if filepath.Dir(k) == filepath.Dir(p) {
					o.Close()
					delete(s.writers, k)
				}
			}
			s.writers[p] = w
		}
		if err := w.write(byPath[p]...); err != nil {
			return err
		}
	}
	return nil
}

// topLabels keeps the largest labels of each group; services ("s:") and
// programs ("p:") are always kept, sites and addresses are trimmed, and the
// rest is summed into "h:" (other).
func topLabels(groups map[string]map[string]Counter, n int) map[string]map[string]Counter {
	for g, m := range groups {
		var sites []string
		for l := range m {
			if strings.HasPrefix(l, "h:") || strings.HasPrefix(l, "a:") {
				sites = append(sites, l)
			}
		}
		if len(sites) <= n {
			continue
		}
		sort.Slice(sites, func(i, j int) bool { return m[sites[i]].Total() > m[sites[j]].Total() })
		other := m["h:"]
		for _, l := range sites[n:] {
			other.Add(m[l])
			delete(m, l)
		}
		m["h:"] = other
		groups[g] = m
	}
	return groups
}

// Add records one second of traffic: series totals and label breakdowns.
func (s *Store) Add(t int64, series map[string]Counter, labels map[string]map[string]Counter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addLive(t, series)

	m := t - t%60
	if s.curMin == nil || s.curMin.T != m {
		if s.curMin != nil {
			s.minutes = append(s.minutes, s.curMin)
			s.pendMin = append(s.pendMin, s.curMin)
		}
		s.curMin = &Bucket{T: m, Series: map[string]Counter{}}
		cut := t - 2*86400
		s.minFrom = max(s.minFrom, cut)
		k := 0
		for k < len(s.minutes) && s.minutes[k].T < cut {
			k++
		}
		s.minutes = s.minutes[k:]
	}
	h := s.hourStart(t)
	if s.curHour == nil || s.curHour.T != h {
		if s.curHour != nil {
			s.finishHour(t)
		}
		s.curHour = newBucket(h)
	}
	for k, c := range series {
		x := s.curMin.Series[k]
		x.Add(c)
		s.curMin.Series[k] = x
		y := s.curHour.Series[k]
		y.Add(c)
		s.curHour.Series[k] = y
	}
	for g, m := range labels {
		dst := s.curHour.Labels[g]
		if dst == nil {
			dst = map[string]Counter{}
			s.curHour.Labels[g] = dst
		}
		for l, c := range m {
			x := dst[intern(l)]
			x.Add(c)
			dst[intern(l)] = x
		}
	}
	if time.Since(s.lastSave) >= s.opt.FlushEvery {
		s.flushMinutes()
	}
}

func (s *Store) flushMinutes() {
	if err := s.writeTo("min", s.pendMin...); err != nil {
		s.log.Warn("store: write minutes", "err", err)
	}
	s.pendMin = nil
	s.lastSave = time.Now()
}

// finishHour closes the current hour; at a day change it also writes the
// finished day and prunes old data.
func (s *Store) finishHour(now int64) {
	h := s.curHour
	h.Labels = topLabels(h.Labels, s.opt.TopLabels)
	s.hours = append(s.hours, h)
	if err := s.writeTo("hour", h); err != nil {
		s.log.Warn("store: write hour", "err", err)
	}
	if s.DayStart(h.T) != s.DayStart(now) {
		s.flushMinutes()
		s.catchUpDays(now)
		s.prune(now)
	}
	cut := now - int64(s.opt.MemDays)*86400
	s.hourFrom = max(s.hourFrom, cut)
	k := 0
	for k < len(s.hours) && s.hours[k].T < cut {
		k++
	}
	s.hours = s.hours[k:]
}

// prune deletes files past their retention.
func (s *Store) prune(now int64) {
	clean := func(kind string, keepDays int, layout string) {
		dir := filepath.Join(s.opt.Dir, kind)
		cut := time.Unix(now, 0).In(s.loc).AddDate(0, 0, -keepDays)
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			name := strings.TrimSuffix(e.Name(), ".wsb")
			t, err := time.ParseInLocation(layout, name, s.loc)
			if err != nil {
				continue
			}
			end := t.AddDate(0, 0, 1)
			if layout == "200601" {
				end = t.AddDate(0, 1, 0)
			}
			if end.Before(cut) {
				os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
	clean("min", s.opt.MinuteDays, "20060102")
	clean("hour", s.opt.HourDays, "200601")
}

// Close writes what is in progress; buckets written now and the rest of the
// same interval later are summed when read back.
func (s *Store) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.curMin != nil && len(s.curMin.Series) > 0 {
		s.pendMin = append(s.pendMin, s.curMin)
	}
	s.flushMinutes()
	if s.curHour != nil && len(s.curHour.Series) > 0 {
		s.curHour.Labels = topLabels(s.curHour.Labels, s.opt.TopLabels)
		if err := s.writeTo("hour", s.curHour); err != nil {
			s.log.Warn("store: write hour", "err", err)
		}
	}
	for _, w := range s.writers {
		w.Close()
	}
	s.writers = map[string]*fileWriter{}
}

func (s *Store) addLive(t int64, series map[string]Counter) {
	n := int64(s.opt.LiveSeconds)
	if s.liveEnd == 0 {
		s.liveEnd = t - 1
	}
	if t <= s.liveEnd {
		// same second twice (clock step back): add to it
		for k, c := range series {
			if r := s.live[k]; r != nil {
				i := t % n
				r.up[i] += uint32(min(c.Up, 1<<32-1))
				r.down[i] += uint32(min(c.Down, 1<<32-1))
			}
		}
		return
	}
	for k, r := range s.live {
		if t-r.last > n {
			delete(s.live, k)
			continue
		}
		for x := s.liveEnd + 1; x <= t && x-s.liveEnd <= n; x++ {
			r.up[x%n], r.down[x%n] = 0, 0
		}
	}
	for k, c := range series {
		r := s.live[k]
		if r == nil {
			r = &ring{up: make([]uint32, n), down: make([]uint32, n)}
			s.live[intern(k)] = r
		}
		r.up[t%n] = uint32(min(c.Up, 1<<32-1))
		r.down[t%n] = uint32(min(c.Down, 1<<32-1))
		r.last = t
	}
	s.liveEnd = t
}

// Live returns 1-second points after since (at most the live window) for
// the keys (all keys with a prefix when a key ends in '*').
func (s *Store) Live(keys []string, since int64) (int64, int64, map[string][]Counter) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := int64(s.opt.LiveSeconds)
	end := s.liveEnd
	from := since + 1
	if from < end-n+1 {
		from = end - n + 1
	}
	out := map[string][]Counter{}
	if end == 0 || from > end {
		return from, end, out
	}
	for _, k := range s.match(keys, func(f func(string)) {
		for k := range s.live {
			f(k)
		}
	}) {
		r := s.live[k]
		pts := make([]Counter, 0, end-from+1)
		for t := from; t <= end; t++ {
			pts = append(pts, Counter{Up: uint64(r.up[t%n]), Down: uint64(r.down[t%n])})
		}
		out[k] = pts
	}
	return from, end, out
}

// match expands keys ending in '*' over the keys the iterator yields.
func (s *Store) match(keys []string, each func(func(string))) []string {
	var exact []string
	var prefixes []string
	for _, k := range keys {
		if p, ok := strings.CutSuffix(k, "*"); ok {
			prefixes = append(prefixes, p)
		} else {
			exact = append(exact, k)
		}
	}
	set := map[string]bool{}
	each(func(k string) {
		for _, e := range exact {
			if k == e {
				set[k] = true
			}
		}
		for _, p := range prefixes {
			if strings.HasPrefix(k, p) {
				set[k] = true
			}
		}
	})
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func matchKey(k string, keys []string) bool {
	for _, x := range keys {
		if p, ok := strings.CutSuffix(x, "*"); ok {
			if strings.HasPrefix(k, p) {
				return true
			}
		} else if k == x {
			return true
		}
	}
	return false
}

// Totals sums series matching keys over [from, to). Ranges within the last
// two days use minutes; longer ones whole hours (and days beyond the hours
// kept in memory).
func (s *Store) Totals(keys []string, from, to int64) map[string]Counter {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]Counter{}
	add := func(b *Bucket) {
		if b == nil || b.T < from || b.T >= to {
			return
		}
		for k, c := range b.Series {
			if matchKey(k, keys) {
				x := out[k]
				x.Add(c)
				out[k] = x
			}
		}
	}
	for _, src := range s.sources(from) {
		for _, b := range src {
			add(b)
		}
	}
	return out
}

// sources picks the bucket lists that cover [from, now) without overlap.
func (s *Store) sources(from int64) [][]*Bucket {
	now := s.liveEnd
	if now == 0 {
		now = time.Now().Unix()
	}
	if from >= s.minFrom {
		return [][]*Bucket{s.minutes, {s.curMin}}
	}
	hoursFrom := s.hourFrom
	if from >= hoursFrom {
		return [][]*Bucket{s.hours, {s.curHour}}
	}
	// whole days before the first whole day the hours cover, then hours
	cut := s.DayStart(hoursFrom)
	if cut != hoursFrom {
		cut = time.Unix(cut, 0).In(s.loc).AddDate(0, 0, 1).Unix()
	}
	var early []*Bucket
	for _, d := range s.days {
		if d.T < cut {
			early = append(early, d)
		}
	}
	var hs []*Bucket
	for _, h := range s.hours {
		if h.T >= cut {
			hs = append(hs, h)
		}
	}
	return [][]*Bucket{early, hs, {s.curHour}}
}

// Point is one bin of a history query.
type Series struct {
	From  int64                // first bin start
	Step  int64                // seconds, or 0 for calendar months
	Bins  []int64              // bin starts
	Data  map[string][]Counter // per key, per bin
	Label string               // resolution used
}

// History bins the series matching keys over [from, to). step is the bin
// size in seconds (60, 300, 3600, 86400...) or -1 for calendar months.
func (s *Store) History(keys []string, from, to, step int64) *Series {
	if step == 0 {
		step = 60
	}
	out := &Series{From: from, Step: step, Data: map[string][]Counter{}}
	// bin boundaries (local days and months for daily and monthly bins)
	switch {
	case step < 0:
		lt := time.Unix(from, 0).In(s.loc)
		t := time.Date(lt.Year(), lt.Month(), 1, 0, 0, 0, 0, s.loc)
		for t.Unix() < to {
			out.Bins = append(out.Bins, t.Unix())
			t = t.AddDate(0, 1, 0)
		}
	case step >= 86400:
		lt := time.Unix(s.DayStart(from), 0).In(s.loc)
		days := int(step / 86400)
		for t := lt; t.Unix() < to; t = t.AddDate(0, 0, days) {
			out.Bins = append(out.Bins, t.Unix())
		}
	default:
		start := from - from%step
		if step >= 3600 {
			start = s.hourStart(from)
		}
		for t := start; t < to; t += step {
			out.Bins = append(out.Bins, t)
		}
	}
	if len(out.Bins) == 0 {
		return out
	}
	out.From = out.Bins[0]
	binOf := func(t int64) int {
		i := sort.Search(len(out.Bins), func(i int) bool { return out.Bins[i] > t }) - 1
		return i
	}
	put := func(k string, i int, c Counter) {
		d := out.Data[k]
		if d == nil {
			d = make([]Counter, len(out.Bins))
			out.Data[k] = d
		}
		d[i].Add(c)
	}

	if step > 0 && step < 60 {
		f, e, live := s.Live(keys, out.From-1)
		for k, pts := range live {
			for j, c := range pts {
				t := f + int64(j)
				if t >= out.From && t < to && t <= e {
					put(k, binOf(t), c)
				}
			}
		}
		out.Label = "second"
		return out
	}

	var older []*Bucket
	if step > 0 && step < 3600 && out.From < s.minFromNow() && to-out.From <= 4*86400 {
		// minute detail older than the minutes in memory: from the day files
		older = s.diskMinutes(out.From, min(to, s.minFromNow()))
	}
	s.mu.RLock()
	var lists [][]*Bucket
	switch {
	case step < 3600 && (out.From >= s.minFrom || older != nil):
		lists = [][]*Bucket{older, s.minutes, {s.curMin}}
		out.Label = "minute"
	default:
		lists = s.sources(out.From)
		out.Label = "hour"
		if len(lists) == 3 {
			out.Label = "day"
		}
	}
	for _, l := range lists {
		for _, b := range l {
			if b == nil || b.T < out.From || b.T >= to {
				continue
			}
			i := binOf(b.T)
			if i < 0 {
				continue
			}
			for k, c := range b.Series {
				if matchKey(k, keys) {
					put(k, i, c)
				}
			}
		}
	}
	s.mu.RUnlock()
	return out
}

func (s *Store) minFromNow() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.minFrom
}

// diskMinutes reads the minute buckets of [from, to) from the day files.
func (s *Store) diskMinutes(from, to int64) []*Bucket {
	var out []*Bucket
	lt := time.Unix(from, 0).In(s.loc)
	for d := time.Date(lt.Year(), lt.Month(), lt.Day(), 0, 0, 0, 0, s.loc); d.Unix() < to; d = d.AddDate(0, 0, 1) {
		bs, err := readBuckets(s.path("min", d.Unix()))
		if err != nil {
			continue
		}
		for _, b := range bs {
			if b.T >= from && b.T < to {
				out = append(out, b)
			}
		}
	}
	if out == nil {
		out = []*Bucket{}
	}
	return out
}

// Labels sums the label breakdowns of the given groups over [from, to).
// Hours older than those in memory are read from the files.
func (s *Store) Labels(groups []string, from, to int64) map[string]map[string]Counter {
	out := map[string]map[string]Counter{}
	add := func(b *Bucket) {
		if b == nil || b.T < from || b.T >= to {
			return
		}
		for g, m := range b.Labels {
			if !matchKey(g, groups) {
				continue
			}
			dst := out[g]
			if dst == nil {
				dst = map[string]Counter{}
				out[g] = dst
			}
			for l, c := range m {
				x := dst[l]
				x.Add(c)
				dst[l] = x
			}
		}
	}
	s.mu.RLock()
	memFrom := s.hourFrom
	for _, h := range s.hours {
		add(h)
	}
	add(s.curHour)
	s.mu.RUnlock()
	if from < memFrom {
		end := min(to, memFrom)
		lt := time.Unix(from, 0).In(s.loc)
		for m := time.Date(lt.Year(), lt.Month(), 1, 0, 0, 0, 0, s.loc); m.Unix() < end; m = m.AddDate(0, 1, 0) {
			bs, err := readBuckets(s.path("hour", m.Unix()))
			if err != nil {
				continue
			}
			for _, b := range bs {
				if b.T < memFrom {
					add(b)
				}
			}
		}
	}
	return out
}

// Keys lists the series keys with a prefix seen in [from, now).
func (s *Store) Keys(prefix string, from int64) []string {
	set := map[string]bool{}
	s.mu.RLock()
	for _, src := range s.sources(from) {
		for _, b := range src {
			if b == nil || b.T < from {
				continue
			}
			for k := range b.Series {
				if strings.HasPrefix(k, prefix) {
					set[k] = true
				}
			}
		}
	}
	s.mu.RUnlock()
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Usage is the disk the files take.
func (s *Store) Usage() (int64, error) {
	var total int64
	err := filepath.Walk(s.opt.Dir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

func (s *Store) String() string { return fmt.Sprintf("store(%s)", s.opt.Dir) }
