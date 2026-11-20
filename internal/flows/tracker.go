// Package flows turns conntrack's per-connection byte counters into
// per-interval deltas: a dump each tick, plus destroy events so connections
// that end between two dumps are still counted.
package flows

import (
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"github.com/mdlayher/netlink"
)

// Key identifies a conntrack entry: its kernel id plus the original tuple, so
// a reused id is a new flow.
type Key struct {
	ID    uint32
	Proto uint8
	Src   netip.AddrPort
	Dst   netip.AddrPort
}

// Flow is one connection as seen in this interval.
type Flow struct {
	Key
	ReplySrc, ReplyDst netip.AddrPort // the reply tuple (after NAT)
	Mark               uint32
	OrigBytes          uint64 // cumulative, original direction
	ReplyBytes         uint64
	DOrig, DReply      uint64 // bytes in this interval
	New                bool   // first seen in this interval
	Done               bool   // ended in this interval
	TCPState           uint8
}

type counters struct{ orig, reply uint64 }

type Tracker struct {
	log    *slog.Logger
	dump   *rawCT
	mu     sync.Mutex
	ended  map[Key]Flow
	prev   map[Key]counters
	primed bool
	stop   chan struct{}
	ev     *netlink.Conn
}

func NewTracker(log *slog.Logger) (*Tracker, error) {
	c, err := openRawCT()
	if err != nil {
		return nil, err
	}
	t := &Tracker{log: log, dump: c, ended: map[Key]Flow{}, prev: map[Key]counters{}, stop: make(chan struct{})}
	go t.listen()
	return t, nil
}

func (t *Tracker) Close() {
	close(t.stop)
	t.mu.Lock()
	if t.ev != nil {
		t.ev.Close()
	}
	t.mu.Unlock()
	t.dump.Close()
}

// listen collects destroy events (with final counters) until Close,
// re-subscribing when the kernel drops the socket (buffer overrun).
func (t *Tracker) listen() {
	for {
		err := t.listenOnce()
		select {
		case <-t.stop:
			return
		default:
		}
		t.log.Warn("conntrack events", "err", err)
		select {
		case <-t.stop:
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (t *Tracker) listenOnce() error {
	c, err := dialCT()
	if err != nil {
		return err
	}
	t.mu.Lock()
	t.ev = c
	t.mu.Unlock()
	defer c.Close()
	c.SetReadBuffer(4 << 20)
	if err := c.JoinGroup(groupCTDestroy); err != nil {
		return err
	}
	for {
		msgs, err := c.Receive()
		if err != nil {
			select {
			case <-t.stop:
				return nil
			default:
			}
			return err
		}
		for _, m := range msgs {
			if uint16(m.Header.Type)&0xff != ctMsgDelete {
				continue
			}
			if f, ok := parseFlow(m.Data); ok {
				t.mu.Lock()
				t.ended[f.Key] = f
				t.mu.Unlock()
			}
		}
	}
}

// Poll dumps the table and returns every flow that moved bytes (or started or
// ended) since the last Poll, and the table size. The first Poll only records
// a baseline.
func (t *Tracker) Poll() ([]Flow, int, error) {
	dumped, err := t.dump.dump()
	if err != nil {
		return nil, 0, err
	}
	t.mu.Lock()
	ended := t.ended
	t.ended = make(map[Key]Flow, len(ended))
	t.mu.Unlock()

	out := make([]Flow, 0, 64)
	next := make(map[Key]counters, len(dumped))
	for i := range dumped {
		f := dumped[i]
		cur := counters{f.OrigBytes, f.ReplyBytes}
		next[f.Key] = cur
		prev, seen := t.prev[f.Key]
		if !t.primed {
			continue
		}
		if !seen {
			f.New = true
		}
		f.DOrig, f.DReply = sub(cur.orig, prev.orig), sub(cur.reply, prev.reply)
		if f.New || f.DOrig > 0 || f.DReply > 0 {
			out = append(out, f)
		}
	}
	if t.primed {
		for k, f := range ended {
			if _, alive := next[k]; alive {
				continue
			}
			prev, seen := t.prev[k]
			f.New = !seen
			f.Done = true
			f.DOrig, f.DReply = sub(f.OrigBytes, prev.orig), sub(f.ReplyBytes, prev.reply)
			out = append(out, f)
		}
	}
	t.prev = next
	t.primed = true
	return out, len(dumped), nil
}

// sub is the growth of a counter; a smaller value means a new entry.
func sub(cur, prev uint64) uint64 {
	if cur >= prev {
		return cur - prev
	}
	return cur
}
