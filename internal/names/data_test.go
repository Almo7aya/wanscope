package names

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
)

// writeOwners writes an owners file the way cmd/wanscope-data does.
func writeOwners(t *testing.T, names []string, starts, owners []uint32) string {
	t.Helper()
	var raw bytes.Buffer
	le := binary.LittleEndian
	u32 := func(v uint32) { binary.Write(&raw, le, v) }
	raw.WriteString("WSOWN1\n")
	u32(1795200000)
	u32(uint32(len(names)))
	for _, n := range names {
		raw.WriteByte(byte(len(n)))
		raw.WriteString(n)
	}
	u32(uint32(len(starts)))
	for _, s := range starts {
		u32(s)
	}
	for _, o := range owners {
		u32(o)
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write(raw.Bytes())
	zw.Close()
	p := filepath.Join(t.TempDir(), "owners.bin.gz")
	if err := os.WriteFile(p, gz.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func ip4(s string) uint32 { b := netip.MustParseAddr(s).As4(); return binary.BigEndian.Uint32(b[:]) }

func TestOwners(t *testing.T) {
	p := writeOwners(t, []string{"Cloudflare", "Amazon.com"},
		[]uint32{ip4("1.0.0.0"), ip4("1.0.1.0"), ip4("18.200.0.0"), ip4("18.201.0.0")},
		[]uint32{1, 0, 2, 0})
	o, err := LoadOwners(p)
	if err != nil {
		t.Fatal(err)
	}
	if o.Built != 1795200000 || o.Len() != 4 {
		t.Fatalf("built %d, %d ranges", o.Built, o.Len())
	}
	for addr, want := range map[string]string{
		"1.0.0.1":               "Cloudflare",
		"1.0.0.255":             "Cloudflare",
		"1.0.1.0":               "", // a gap
		"18.200.121.106":        "Amazon.com",
		"18.201.0.1":            "",
		"0.255.255.255":         "", // before the first range
		"192.168.1.10":          "", // private: never anyone's
		"::ffff:18.200.121.106": "Amazon.com",
	} {
		if got := o.Lookup(netip.MustParseAddr(addr)); got != want {
			t.Errorf("Lookup(%s) = %q, want %q", addr, got, want)
		}
	}
	var none *Owners
	if none.Lookup(netip.MustParseAddr("1.1.1.1")) != "" || none.Len() != 0 {
		t.Error("a missing table names nobody")
	}
}

func TestOwnersRejectsOtherFiles(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.gz")
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write([]byte("not an owners file"))
	zw.Close()
	os.WriteFile(p, gz.Bytes(), 0o644)
	if _, err := LoadOwners(p); err == nil {
		t.Error("LoadOwners took a file that isn't one")
	}
}

func TestExtraServicesComeAfterBuiltin(t *testing.T) {
	r := NewRules(nil, []string{"Epic Games|games|easyanticheat.net epicgames.com", "Example|other|example.org"})
	if s := r.Classify("modules.easyanticheat.net", "", netip.Addr{}, 6, 443); s == nil || s.Name != "Easy Anti-Cheat" {
		t.Errorf("the built-in name should win, got %v", s)
	}
	if s := r.Classify("cdn.example.org", "", netip.Addr{}, 6, 443); s == nil || s.Name != "Example" {
		t.Errorf("an extra service should name its domains, got %v", s)
	}
	r = NewRules([]CustomRule{{Name: "Mine", Domains: []string{"example.org"}}}, []string{"Example|other|example.org"})
	if s := r.Classify("example.org", "", netip.Addr{}, 6, 443); s == nil || s.Name != "Mine" {
		t.Errorf("your rule should win, got %v", s)
	}
}
