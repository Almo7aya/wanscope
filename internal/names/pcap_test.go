package names

import (
	"encoding/binary"
	"os"
	"testing"
	"time"
)

// TestPcap feeds a capture (WANSCOPE_PCAP=file.pcap, Ethernet) through the
// parsers and reports what each packet gave.
func TestPcap(t *testing.T) {
	path := os.Getenv("WANSCOPE_PCAP")
	if path == "" {
		t.Skip("set WANSCOPE_PCAP")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	q := newQuicAssembler()
	c := NewCache()
	p := 24
	n := 0
	for p+16 <= len(raw) {
		incl := int(binary.LittleEndian.Uint32(raw[p+8:]))
		p += 16
		if p+incl > len(raw) {
			break
		}
		frame := raw[p : p+incl]
		p += incl
		n++
		pk, ok := parseFrame(frame)
		if !ok {
			t.Logf("%d: not parsed", n)
			continue
		}
		c.Handle(pk)
		if pk.Proto == 17 && pk.Dst.Port() == 443 {
			dcid, plain, ok := initialPayload(pk.Payload)
			if !ok {
				t.Logf("%d: %v -> %v: not a decryptable initial (first byte %#x, len %d)", n, pk.Src, pk.Dst, pk.Payload[0], len(pk.Payload))
				continue
			}
			chunks := cryptoFrames(plain)
			sni, done := q.add(pk.Src, pk.Payload, time.Now())
			t.Logf("%d: %v -> %v dcid %x: %d crypto chunks %v -> %q %v", n, pk.Src, pk.Dst, dcid, len(chunks), offs(chunks), sni, done)
		}
	}
	t.Logf("%d packets, stats %+v", n, c.Stats())
}

func offs(c []cryptoChunk) []uint64 {
	var o []uint64
	for _, x := range c {
		o = append(o, x.off, uint64(len(x.data)))
	}
	return o
}
