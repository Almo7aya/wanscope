package engine

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Almo7aya/wanscope/internal/names"
)

// dataFiles holds the optional data package's lists (names.DataDir), read again only when a file changes: the
// owners table takes a few MB and a moment to read.
type dataFiles struct {
	mu        sync.Mutex
	services  []string
	svcMod    time.Time
	owners    atomic.Pointer[names.Owners]
	ownersMod time.Time
}

// loadData (re)reads the data package's files that changed and returns its services for names.NewRules.
func (e *Engine) loadData() []string {
	d := &e.data
	d.mu.Lock()
	defer d.mu.Unlock()
	if p := filepath.Join(names.DataDir, "services.txt"); changed(p, &d.svcMod) {
		l, err := names.LoadServices(p)
		if err != nil {
			e.log.Warn("data package: services", "err", err)
		} else {
			d.services = l
			e.log.Info("data package: services loaded", "services", len(l))
		}
	}
	if p := filepath.Join(names.DataDir, "owners.bin.gz"); changed(p, &d.ownersMod) {
		o, err := names.LoadOwners(p)
		if err != nil {
			e.log.Warn("data package: owners", "err", err)
		} else {
			d.owners.Store(o)
			e.log.Info("data package: address owners loaded", "ranges", o.Len())
		}
	}
	return d.services
}

// changed reports whether path's modification time differs from *seen (and records it); a missing file is not a
// change.
func changed(path string, seen *time.Time) bool {
	st, err := os.Stat(path)
	if err != nil || st.ModTime().Equal(*seen) {
		return false
	}
	*seen = st.ModTime()
	return true
}

// dataStatus says what the data package gives, for the pages (and their credits).
func (e *Engine) dataStatus() map[string]any {
	d := &e.data
	d.mu.Lock()
	n := len(d.services)
	d.mu.Unlock()
	o := d.owners.Load()
	out := map[string]any{"services": n, "owners": o.Len()}
	if o != nil {
		out["owners_built"] = o.Built
	}
	return out
}
