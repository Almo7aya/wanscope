// Package engine ties the collectors together: every second it reads the
// interface counters and conntrack, attributes each connection's bytes to a
// device, an uplink and a service, and stores the result.
package engine

import (
	"context"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Almo7aya/wanscope/internal/flows"
	"github.com/Almo7aya/wanscope/internal/names"
	"github.com/Almo7aya/wanscope/internal/procs"
	"github.com/Almo7aya/wanscope/internal/store"
	"github.com/Almo7aya/wanscope/internal/sys"
	"github.com/Almo7aya/wanscope/internal/ubus"
)

var Version = "dev"

// FlowState is what wanscope knows about one connection.
type FlowState struct {
	Key       flows.Key
	Dev       string
	WAN       string
	Local     netip.AddrPort // the device's (or router's) end
	Remote    netip.AddrPort
	Proto     uint8
	UpIsOrig  bool
	LocalOnly bool // between the LAN and the router: not internet traffic
	Host      string
	Alias     string
	NameSrc   string
	Service   string
	Category  string
	Site      string
	Proc      string
	Tunnel    string // the tunnel this connection carries (WireGuard transport)
	Org       string // who announces the server's address (the data package's owners table)
	Ruled     bool   // a rule (yours, built-in or the data package's) named its service
	topo      *Topology
	First     int64
	Last      int64
	Up, Down  uint64
	RateUp    uint64
	RateDown  uint64
	tries     int
}

type Engine struct {
	log     *slog.Logger
	bus     *ubus.Bus
	nl      *sys.Netlink
	tracker *flows.Tracker
	names   *names.Cache
	sniffer *names.Sniffer
	procs   *procs.Resolver
	Store   *store.Store
	Devs    *Registry

	cfg   atomic.Pointer[Config]
	topo  atomic.Pointer[Topology]
	rules atomic.Pointer[names.Rules]
	data  dataFiles // the optional data package (data.go)

	mu          sync.RWMutex
	flows       map[flows.Key]*FlowState
	rates       map[string]store.Counter // series of the last tick, per second
	nflows      int
	linkPrev    map[string]sys.LinkStats
	lastTick    int64
	started     int64
	warnings    []string
	sniffDevs   []string
	tickCost    time.Duration
	pollCost    time.Duration
	lastPoll    int64
	polled      bool
	viewerUntil atomic.Int64              // a page asked for live data: read conntrack every second
	tunEnds     map[netip.AddrPort]string // WireGuard peer endpoint -> interface
	tunProcs    map[string]string         // program relaying a tunnel -> interface

	policy  *Policy
	alerts  *Alerts
	blocks  *svcBlocks // blocked services' addresses (svcblock.go)
	buys    *purchases
	updates *updater // new releases (update.go)
}

func New(log *slog.Logger, cfg *Config) (*Engine, error) {
	loc := sys.Location()
	st, err := store.Open(store.Options{
		Dir: cfg.DataDir, LiveSeconds: cfg.LiveSeconds, MinuteDays: cfg.MinuteDays,
		HourDays: cfg.HourDays, MemDays: cfg.MemDays,
	}, loc, log)
	if err != nil {
		return nil, err
	}
	nl, err := sys.OpenNetlink()
	if err != nil {
		return nil, err
	}
	tr, err := flows.NewTracker(log)
	if err != nil {
		return nil, err
	}
	e := &Engine{
		log: log, nl: nl, tracker: tr, names: names.NewCache(), procs: procs.NewResolver(), Store: st,
		Devs: NewRegistry(cfg.DataDir), flows: map[flows.Key]*FlowState{}, rates: map[string]store.Counter{},
		linkPrev: map[string]sys.LinkStats{}, started: time.Now().Unix(), buys: loadPurchases(cfg.DataDir),
		blocks: newSvcBlocks(),
	}
	e.names.OnAnswer = e.blocks.dnsAnswer
	e.guestSince(cfg)
	e.cfg.Store(cfg)
	e.rules.Store(names.NewRules(cfg.Rules, e.loadData()))
	e.policy = newPolicy(e)
	e.alerts = newAlerts(e)
	e.updates = newUpdater(e)
	e.bus = ubus.NewBus("", log, e.ubusObject())
	return e, nil
}

func (e *Engine) Config() *Config { return e.cfg.Load() }

// Reload re-reads the configuration (storage settings need a restart).
func (e *Engine) Reload() error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	e.guestSince(cfg)
	e.cfg.Store(cfg)
	rules := names.NewRules(cfg.Rules, e.loadData())
	e.rules.Store(rules)
	// live connections take new rules at once (a service named on the Activity page shows as such right away)
	e.mu.Lock()
	for _, fs := range e.flows {
		if !fs.LocalOnly {
			e.label(fs, rules)
		}
	}
	e.mu.Unlock()
	e.refreshTopology()
	e.policy.dirty.Store(true)
	e.log.Info("config reloaded")
	return nil
}

func (e *Engine) Run(ctx context.Context) {
	go e.bus.Run(ctx)
	// ubus is needed for the topology
	for i := 0; i < 50 && ctx.Err() == nil; i++ {
		if e.refreshTopology() == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	e.refreshClients(time.Now().Unix())
	e.refreshTunnels()
	if e.Config().Sniff {
		if p := adguardLog(); p != "" {
			e.log.Info("names from AdGuard Home's log", "answers", e.names.SeedAdGuard(p, 16<<20))
		}
	}
	e.startSniffer()
	e.checkSystem()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); e.slowLoop(ctx) }()

	interval := e.Config().Interval
	next := time.Now().Truncate(time.Second).Add(time.Second)
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			e.shutdown()
			return
		case <-time.After(time.Until(next)):
		}
		now := next.Unix()
		next = next.Add(interval)
		if time.Until(next) < 0 {
			// fell behind (suspend, clock step): realign
			next = time.Now().Truncate(time.Second).Add(time.Second)
		}
		t0 := time.Now()
		e.tick(now, int64(interval/time.Second))
		e.mu.Lock()
		e.tickCost = time.Since(t0)
		e.mu.Unlock()
	}
}

func (e *Engine) shutdown() {
	if e.sniffer != nil {
		e.sniffer.Stop()
	}
	e.Store.Close()
	e.Devs.Save(true)
	e.alerts.save()
	e.policy.clear()
	e.tracker.Close()
	e.log.Info("stopped")
}

// slowLoop refreshes what changes rarely.
func (e *Engine) slowLoop(ctx context.Context) {
	t5 := time.NewTicker(5 * time.Second)
	t30 := time.NewTicker(30 * time.Second)
	defer t5.Stop()
	defer t30.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t5.C:
			now := time.Now().Unix()
			e.refreshClients(now)
			e.alerts.check(now)
			e.Devs.Save(false)
		case <-t30.C:
			e.refreshTopology()
			e.refreshTunnels()
			e.discoverNames(time.Now().Unix())
			e.checkSystem()
			e.updates.tick(time.Now().Unix())
		}
	}
}

func (e *Engine) refreshTopology() error {
	t, err := LoadTopology(e.bus, e.nl, e.Config())
	if err != nil {
		return err
	}
	e.topo.Store(t)
	return nil
}

func (e *Engine) Topo() *Topology {
	if t := e.topo.Load(); t != nil {
		return t
	}
	return &Topology{byName: map[string]*Iface{}, byDev: map[string]*Iface{}, router: map[netip.Addr]string{}}
}

func (e *Engine) refreshClients(now int64) {
	neigh, err := e.nl.Neighbors()
	if err != nil {
		e.log.Warn("neighbours", "err", err)
	}
	leases := sys.ReadLeases("/tmp/dhcp.leases")
	fresh := e.Devs.Refresh(neigh, leases, sys.StaticNames(), e.topo.Load(), now)
	for _, k := range fresh {
		e.alerts.newDevice(k, now)
	}
	if len(fresh) > 0 {
		e.policy.dirty.Store(true)
	}
}

func (e *Engine) startSniffer() {
	cfg := e.Config()
	if !cfg.Sniff {
		return
	}
	devs := cfg.SniffDevs
	if len(devs) == 0 {
		devs = e.Topo().LANDevs()
	}
	e.sniffer = names.NewSniffer(e.log, e.names.Handle)
	for _, d := range devs {
		go e.sniffer.Watch(d)
	}
	e.mu.Lock()
	e.sniffDevs = devs
	e.mu.Unlock()
}

// checkSystem looks for settings that make the counts wrong.
func (e *Engine) checkSystem() {
	var w []string
	if v := readSysctl("net/netfilter/nf_conntrack_acct"); v == "0" {
		if writeSysctl("net/netfilter/nf_conntrack_acct", "1") != nil {
			w = append(w, "Connection byte counting (nf_conntrack_acct) is off: per-device numbers stay at zero.")
		}
	}
	if offloadOn() {
		w = append(w, "Flow offloading is on: offloaded connections skip the firewall, so their traffic and the limits are not seen. Turn it off in Network → Firewall.")
	}
	e.mu.Lock()
	e.warnings = w
	e.mu.Unlock()
}

func (e *Engine) tick(now, secs int64) {
	cfg := e.Config()
	topo := e.Topo()
	series := map[string]store.Counter{}
	labels := map[string]map[string]store.Counter{}
	add := func(k string, c store.Counter) {
		x := series[k]
		x.Add(c)
		series[k] = x
	}
	label := func(g, l string, c store.Counter) {
		m := labels[g]
		if m == nil {
			m = map[string]store.Counter{}
			labels[g] = m
		}
		x := m[l]
		x.Add(c)
		m[l] = x
	}

	// interface counters: the truth for each uplink
	if stats, err := sys.ReadLinkStats(); err == nil {
		for _, ifc := range topo.Ifaces {
			if ifc.Dev == "" {
				continue
			}
			cur, ok := stats[ifc.Dev]
			if !ok {
				continue
			}
			prev, seen := e.linkPrev[ifc.Dev]
			e.linkPrev[ifc.Dev] = cur
			if !seen {
				continue
			}
			tx := sub(cur.TxBytes, prev.TxBytes) - min(sub(cur.TxBytes, prev.TxBytes), sub(cur.TxPackets, prev.TxPackets)*ifc.Overhead)
			rx := sub(cur.RxBytes, prev.RxBytes) - min(sub(cur.RxBytes, prev.RxBytes), sub(cur.RxPackets, prev.RxPackets)*ifc.Overhead)
			if tx > 0 || rx > 0 {
				add("i:"+ifc.Name, store.Counter{Up: tx, Down: rx})
			}
		}
	}

	// connections: every second while someone looks, else every IdlePoll
	// seconds (destroy events still catch short ones in between)
	var deltas []flows.Flow
	span := int64(1)
	if poll := now < e.viewerUntil.Load() || now-e.lastPoll >= cfg.IdlePoll; poll {
		tp := time.Now()
		var n int
		var err error
		deltas, n, err = e.tracker.Poll()
		if err != nil {
			e.log.Warn("conntrack", "err", err)
		}
		span = max(1, min(now-e.lastPoll, 60))
		if e.lastPoll == 0 {
			span = secs
		}
		e.lastPoll = now
		e.mu.Lock()
		e.nflows = n
		e.pollCost = time.Since(tp)
		for k := range e.flows {
			e.flows[k].RateUp, e.flows[k].RateDown = 0, 0
		}
		e.polled = true
		e.mu.Unlock()
	}
	rules := e.rules.Load()
	e.mu.Lock()
	for i := range deltas {
		f := &deltas[i]
		fs := e.flows[f.Key]
		if fs == nil {
			fs = e.classify(f, topo, now)
			e.flows[f.Key] = fs
		} else if fs.topo != topo {
			e.place(fs, f, topo)
		}
		if fs.Host == "" && fs.tries < 10 && !fs.LocalOnly {
			e.name(fs, rules)
		}
		up, down := f.DOrig, f.DReply
		if !fs.UpIsOrig {
			up, down = down, up
		}
		fs.Last = now
		fs.Up += up
		fs.Down += down
		fs.RateUp, fs.RateDown = up/uint64(span), down/uint64(span)
		if f.Done {
			delete(e.flows, f.Key)
		}
		if up == 0 && down == 0 {
			continue
		}
		c := store.Counter{Up: up, Down: down}
		if fs.LocalOnly {
			if fs.Dev != "" {
				add("l:"+fs.Dev, c)
			}
			continue
		}
		if fs.Tunnel != "" {
			// a tunnel's transport: the same bytes as the traffic inside it, which is
			// already counted for the devices that sent it; kept apart per uplink
			if fs.WAN != "" {
				add("t:"+fs.WAN+"|"+fs.Tunnel, c)
				label("w:"+fs.WAN, "s:"+fs.Service, c)
			}
			continue
		}
		if fs.Dev == "" {
			continue
		}
		add("d:"+fs.Dev, c)
		if fs.WAN != "" {
			add("dw:"+fs.Dev+"|"+fs.WAN, c)
			label("w:"+fs.WAN, "s:"+fs.Service, c)
		}
		label(fs.Dev, "s:"+fs.Service, c)
		if fs.Site != "" {
			label(fs.Dev, "h:"+fs.Site, c)
		} else {
			label(fs.Dev, "a:"+fs.Remote.Addr().String(), c)
		}
		if fs.Dev == RouterKey {
			p := fs.Proc
			if p == "" {
				p = "?"
			}
			label(fs.Dev, "p:"+p, c)
		}
		if fs.Dev != RouterKey {
			e.Devs.Seen(fs.Dev, fs.Local.Addr(), now)
		}
	}
	// forget quiet connections; they come back if they move again
	for k, fs := range e.flows {
		if now-fs.Last > 300 {
			delete(e.flows, k)
		}
	}
	e.learnBlocked(cfg, rules, now)
	// rates per second: links from this tick, connections from the last read
	rates := make(map[string]store.Counter, len(series))
	for k, c := range e.rates {
		if !strings.HasPrefix(k, "i:") && !e.polled {
			rates[k] = c
		}
	}
	for k, c := range series {
		d := uint64(secs)
		if !strings.HasPrefix(k, "i:") {
			d = uint64(span)
		}
		rates[k] = store.Counter{Up: c.Up / d, Down: c.Down / d}
	}
	e.polled = false
	e.rates = rates
	e.lastTick = now
	e.mu.Unlock()

	if secs <= 1 {
		e.Store.Add(now, series, labels)
	} else {
		// spread a longer interval over its seconds so live rates stay right
		for s := secs - 1; s >= 0; s-- {
			part := map[string]store.Counter{}
			for k, c := range series {
				part[k] = store.Counter{Up: c.Up / uint64(secs), Down: c.Down / uint64(secs)}
				if s == 0 {
					part[k] = store.Counter{Up: c.Up - c.Up/uint64(secs)*uint64(secs-1), Down: c.Down - c.Down/uint64(secs)*uint64(secs-1)}
				}
			}
			var lb map[string]map[string]store.Counter
			if s == 0 {
				lb = labels
			}
			e.Store.Add(now-s, part, lb)
		}
	}
	_ = cfg
	e.policy.tick(now)
}

func sub(a, b uint64) uint64 {
	if a >= b {
		return a - b
	}
	return a
}

// classify starts the state of a new connection.
func (e *Engine) classify(f *flows.Flow, topo *Topology, now int64) *FlowState {
	fs := &FlowState{Key: f.Key, Proto: f.Proto, First: now, Last: now}
	e.place(fs, f, topo)
	return fs
}

// place works out whose connection this is, which way is upload, and which
// uplink it uses. It runs again when the interfaces change.
func (e *Engine) place(fs *FlowState, f *flows.Flow, topo *Topology) {
	fs.topo = topo
	fs.Dev, fs.LocalOnly, fs.WAN, fs.Proc, fs.Tunnel = "", false, "", "", ""
	src, dst := f.Src.Addr(), f.Dst.Addr()
	rsrc, rdst := f.ReplySrc.Addr(), f.ReplyDst.Addr()
	switch {
	case src.IsUnspecified() || dst.IsMulticast() || dst == netip.AddrFrom4([4]byte{255, 255, 255, 255}) || src.IsLinkLocalUnicast():
		// DHCP discovery, broadcasts, multicast: nobody's internet traffic
		fs.LocalOnly, fs.Local, fs.Remote = true, f.Src, f.Dst
		return
	case topo.IsRouter(src):
		fs.Dev, fs.UpIsOrig, fs.Local, fs.Remote = RouterKey, true, f.Src, f.Dst
		fs.LocalOnly = topo.IsRouter(dst) || topo.IsLAN(dst)
	case topo.IsLAN(src):
		fs.Dev, fs.UpIsOrig, fs.Local, fs.Remote = e.Devs.KeyOf(src), true, f.Src, f.Dst
		fs.LocalOnly = topo.IsRouter(dst) || topo.IsLAN(dst)
	case topo.IsRouter(dst):
		// to the router from outside (VPN peers, port-forwarded services)
		fs.Dev, fs.UpIsOrig, fs.Local, fs.Remote = RouterKey, false, f.Dst, f.Src
	case topo.IsLAN(rsrc) && !topo.IsRouter(rsrc):
		// a port forward to a LAN device
		fs.Dev, fs.UpIsOrig, fs.Local, fs.Remote = e.Devs.KeyOf(rsrc), false, f.ReplySrc, f.Src
	default:
		// routed for another network (VPN peers...): keyed by address
		fs.Dev, fs.UpIsOrig, fs.Local, fs.Remote = "ip:"+src.String(), true, f.Src, f.Dst
	}
	if fs.Dev == RouterKey {
		fs.Proc = e.procs.Lookup(f.Proto, fs.Local)
		if t, ok := e.tunEnds[fs.Remote]; ok {
			fs.Tunnel = t
		} else if t, ok := e.tunProcs[fs.Proc]; ok && fs.Proc != "" && !fs.LocalOnly {
			fs.Tunnel = t
		}
	}
	if !fs.LocalOnly {
		switch {
		case wanOf(topo.IfaceOfAddr(rdst)) != "":
			fs.WAN = wanOf(topo.IfaceOfAddr(rdst))
		case wanOf(topo.IfaceOfAddr(dst)) != "":
			fs.WAN = wanOf(topo.IfaceOfAddr(dst))
		case wanOf(topo.IfaceOfAddr(src)) != "":
			fs.WAN = wanOf(topo.IfaceOfAddr(src))
		default:
			fs.WAN = topo.WANOfMark(f.Mark)
		}
	}
}
func wanOf(i *Iface) string {
	if i == nil || i.Role != "wan" {
		return ""
	}
	return i.Name
}

// name looks up the server's name; it is retried for a few seconds because
// the ClientHello can arrive after conntrack has the connection.
func (e *Engine) name(fs *FlowState, rules *names.Rules) {
	fs.tries++
	n := e.names.Lookup(fs.Proto, fs.Local, fs.Remote)
	if n.Host != "" {
		fs.Host, fs.Alias, fs.NameSrc = n.Host, n.Alias, n.Source
		fs.Site = names.Site(n.Host)
		fs.tries = 100
	}
	if fs.Org == "" {
		fs.Org = e.data.owners.Load().Lookup(fs.Remote.Addr())
	}
	e.label(fs, rules)
}

// label names a connection's service from what is known: a rule, else its site, else a port with a meaning of
// its own (DNS, mail...), else who owns the server's address, else a plain web port, else "Other".
func (e *Engine) label(fs *FlowState, rules *names.Rules) {
	svc := rules.Classify(fs.Host, fs.Alias, fs.Remote.Addr(), fs.Proto, fs.Remote.Port())
	fs.Ruled = svc != nil
	p := names.PortName(fs.Proto, fs.Remote.Port())
	switch {
	case svc != nil:
		fs.Service, fs.Category = svc.Name, svc.Category
	case fs.Site != "":
		fs.Service, fs.Category = fs.Site, "web"
	case p != "" && !names.WebPort(p):
		fs.Service, fs.Category = p, "other"
	case fs.Org != "":
		fs.Service, fs.Category = fs.Org+" network", "other"
	case p != "":
		fs.Service, fs.Category = p, "other"
	default:
		fs.Service, fs.Category = "Other", "other"
	}
	if fs.Tunnel != "" {
		fs.Service, fs.Category = "Tunnel "+fs.Tunnel, "tunnel"
		if fs.Proc == "" {
			fs.Proc = "wireguard"
		}
		return
	}
	if fs.Dev == RouterKey && fs.Proc != "" && fs.Host == "" && svc == nil {
		fs.Service, fs.Category = fs.Proc, "router"
	}
}
