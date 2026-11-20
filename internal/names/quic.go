package names

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"net/netip"
	"sync"
	"time"
)

// QUIC Initial packets are encrypted with keys anyone can derive from the
// connection id in the clear (RFC 9001 §5.2), so a router can read the
// ClientHello inside them. Chrome spreads that hello over several packets
// and shuffles its CRYPTO frames, so the stream is put back together per
// connection id before reading it.

var (
	saltV1 = []byte{0x38, 0x76, 0x2c, 0xf7, 0xf5, 0x59, 0x34, 0xb3, 0x4d, 0x17, 0x9a, 0xe6, 0xa4, 0xc8, 0x0c, 0xad, 0xcc, 0xbb, 0x7f, 0x0a}
	saltV2 = []byte{0x0d, 0xed, 0xe3, 0xde, 0xf7, 0x00, 0xa6, 0xdb, 0x81, 0x93, 0x81, 0xbe, 0x6e, 0x26, 0x9d, 0xcb, 0xf9, 0xbd, 0x2e, 0xd9}
)

const (
	quicV1 = 0x00000001
	quicV2 = 0x6b3343cf
)

func expandLabel(secret []byte, label string, n int) []byte {
	full := "tls13 " + label
	info := make([]byte, 0, 4+len(full))
	info = binary.BigEndian.AppendUint16(info, uint16(n))
	info = append(info, byte(len(full)))
	info = append(info, full...)
	info = append(info, 0)
	out, err := hkdf.Expand(sha256.New, secret, string(info), n)
	if err != nil {
		return nil
	}
	return out
}

type initialKeys struct {
	aead cipher.AEAD
	iv   []byte
	hp   cipher.Block
}

func clientKeys(version uint32, dcid []byte) *initialKeys {
	salt, kl, il, hl := saltV1, "quic key", "quic iv", "quic hp"
	if version == quicV2 {
		salt, kl, il, hl = saltV2, "quicv2 key", "quicv2 iv", "quicv2 hp"
	}
	initial, err := hkdf.Extract(sha256.New, dcid, salt)
	if err != nil {
		return nil
	}
	client := expandLabel(initial, "client in", 32)
	key, iv, hp := expandLabel(client, kl, 16), expandLabel(client, il, 12), expandLabel(client, hl, 16)
	if key == nil || iv == nil || hp == nil {
		return nil
	}
	kb, err := aes.NewCipher(key)
	if err != nil {
		return nil
	}
	aead, err := cipher.NewGCM(kb)
	if err != nil {
		return nil
	}
	hb, err := aes.NewCipher(hp)
	if err != nil {
		return nil
	}
	return &initialKeys{aead: aead, iv: iv, hp: hb}
}

func varint(b []byte) (uint64, int) {
	if len(b) == 0 {
		return 0, 0
	}
	n := 1 << (b[0] >> 6)
	if len(b) < n {
		return 0, 0
	}
	v := uint64(b[0] & 0x3f)
	for i := 1; i < n; i++ {
		v = v<<8 | uint64(b[i])
	}
	return v, n
}

// initialPayload decrypts a client Initial packet; it returns the
// destination connection id and the plaintext frames.
func initialPayload(pkt []byte) ([]byte, []byte, bool) {
	if len(pkt) < 7 || pkt[0]&0xc0 != 0xc0 {
		return nil, nil, false
	}
	version := binary.BigEndian.Uint32(pkt[1:])
	ptype := (pkt[0] >> 4) & 3
	switch {
	case version == quicV1 && ptype == 0:
	case version == quicV2 && ptype == 1:
	default:
		return nil, nil, false
	}
	p := 5
	dl := int(pkt[p])
	p++
	if dl == 0 || dl > 20 || p+dl >= len(pkt) {
		return nil, nil, false
	}
	dcid := pkt[p : p+dl]
	p += dl
	sl := int(pkt[p])
	p += 1 + sl
	if p >= len(pkt) {
		return nil, nil, false
	}
	tl, n := varint(pkt[p:])
	if n == 0 {
		return nil, nil, false
	}
	p += n + int(tl)
	length, n := varint(pkt[p:])
	if n == 0 {
		return nil, nil, false
	}
	p += n
	pnOff := p
	end := pnOff + int(length)
	if end > len(pkt) || pnOff+20 > len(pkt) {
		return nil, nil, false
	}
	keys := clientKeys(version, dcid)
	if keys == nil {
		return nil, nil, false
	}
	var mask [16]byte
	keys.hp.Encrypt(mask[:], pkt[pnOff+4:pnOff+20])
	hdr := make([]byte, pnOff+4)
	copy(hdr, pkt[:pnOff+4])
	hdr[0] ^= mask[0] & 0x0f
	pnLen := int(hdr[0]&3) + 1
	var pn uint64
	for i := 0; i < pnLen; i++ {
		hdr[pnOff+i] ^= mask[1+i]
		pn = pn<<8 | uint64(hdr[pnOff+i])
	}
	hdr = hdr[:pnOff+pnLen]
	nonce := make([]byte, 12)
	copy(nonce, keys.iv)
	for i := 0; i < 8; i++ {
		nonce[11-i] ^= byte(pn >> (8 * i))
	}
	plain, err := keys.aead.Open(nil, nonce, pkt[pnOff+pnLen:end], hdr)
	if err != nil {
		return nil, nil, false
	}
	return append([]byte(nil), dcid...), plain, true
}

type cryptoChunk struct {
	off  uint64
	data []byte
}

// cryptoFrames picks the CRYPTO frames out of decrypted Initial frames.
func cryptoFrames(b []byte) []cryptoChunk {
	var out []cryptoChunk
	for len(b) > 0 {
		t, n := varint(b)
		if n == 0 {
			return out
		}
		b = b[n:]
		switch {
		case t == 0x00 || t == 0x01: // padding, ping
		case t == 0x02 || t == 0x03: // ack
			vals := 4
			for i := 0; i < vals; i++ {
				v, n := varint(b)
				if n == 0 {
					return out
				}
				b = b[n:]
				if i == 2 {
					vals += 2 * int(v) // gap and length per extra range
					if vals > 1000 {
						return out
					}
				}
			}
			if t == 0x03 {
				for i := 0; i < 3; i++ {
					if _, n := varint(b); n == 0 {
						return out
					} else {
						b = b[n:]
					}
				}
			}
		case t == 0x06:
			off, n := varint(b)
			if n == 0 {
				return out
			}
			b = b[n:]
			l, n := varint(b)
			if n == 0 || int(l) > len(b)-n {
				return out
			}
			b = b[n:]
			out = append(out, cryptoChunk{off: off, data: b[:l]})
			b = b[l:]
		case t == 0x1c || t == 0x1d: // connection close
			for i := 0; i < 2; i++ {
				if t == 0x1d && i == 1 {
					break
				}
				if _, n := varint(b); n == 0 {
					return out
				} else {
					b = b[n:]
				}
			}
			l, n := varint(b)
			if n == 0 || int(l) > len(b)-n {
				return out
			}
			b = b[n+int(l):]
		default:
			return out
		}
	}
	return out
}

type quicHello struct {
	srcs   []netip.AddrPort // every sender of this hello (a device and its NAT copy)
	chunks []cryptoChunk
	seen   time.Time
	done   bool
}

// quicAssembler collects the CRYPTO stream of client Initials per
// connection id until the ClientHello's server name can be read.
type quicAssembler struct {
	mu    sync.Mutex
	conns map[string]*quicHello
	last  time.Time
}

func newQuicAssembler() *quicAssembler { return &quicAssembler{conns: map[string]*quicHello{}} }

// add feeds one UDP payload from src; it returns the server name and its senders once known.
func (q *quicAssembler) add(src netip.AddrPort, payload []byte, now time.Time) (string, []netip.AddrPort) {
	dcid, plain, ok := initialPayload(payload)
	if !ok {
		return "", nil
	}
	chunks := cryptoFrames(plain)
	if len(chunks) == 0 {
		return "", nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if now.Sub(q.last) > 5*time.Second {
		for k, c := range q.conns {
			if now.Sub(c.seen) > 10*time.Second {
				delete(q.conns, k)
			}
		}
		q.last = now
	}
	key := string(dcid)
	c := q.conns[key]
	if c == nil {
		if len(q.conns) > 4096 {
			return "", nil
		}
		c = &quicHello{}
		q.conns[key] = c
	}
	if !containsAP(c.srcs, src) {
		c.srcs = append(c.srcs, src)
	}
	if c.done {
		return "", nil
	}
	c.seen = now
	for _, ch := range chunks {
		if ch.off < 16384 {
			c.chunks = append(c.chunks, cryptoChunk{off: ch.off, data: append([]byte(nil), ch.data...)})
		}
	}
	stream := contiguous(c.chunks)
	sni, _ := ClientHello(stream)
	if sni == "" {
		return "", nil
	}
	c.done, c.chunks = true, nil
	return sni, c.srcs
}

func containsAP(l []netip.AddrPort, a netip.AddrPort) bool {
	for _, x := range l {
		if x == a {
			return true
		}
	}
	return false
}

// contiguous returns the stream bytes from offset 0 up to the first gap.
func contiguous(chunks []cryptoChunk) []byte {
	var out []byte
	for progress := true; progress; {
		progress = false
		for _, c := range chunks {
			end := c.off + uint64(len(c.data))
			if c.off <= uint64(len(out)) && end > uint64(len(out)) {
				out = append(out, c.data[uint64(len(out))-c.off:]...)
				progress = true
			}
		}
	}
	return out
}
