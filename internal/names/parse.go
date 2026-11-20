package names

import (
	"encoding/binary"
	"net/netip"
	"strings"
)

// DNSAnswer is one address a DNS response gave for a name.
type DNSAnswer struct {
	IP    netip.Addr
	Name  string // the name the client asked for
	CNAME string // the last alias on the way, if any (googlevideo.com…)
	TTL   uint32
}

// ParseDNS reads the address answers of a DNS response.
func ParseDNS(b []byte) []DNSAnswer {
	if len(b) < 12 || b[2]&0x80 == 0 || b[3]&0x0f != 0 {
		return nil // not a response, or an error
	}
	qd := int(binary.BigEndian.Uint16(b[4:]))
	an := int(binary.BigEndian.Uint16(b[6:]))
	if qd != 1 || an == 0 {
		return nil
	}
	off := 12
	qname, off, ok := dnsName(b, off)
	if !ok || off+4 > len(b) {
		return nil
	}
	off += 4
	var out []DNSAnswer
	cname := ""
	for i := 0; i < an && off < len(b); i++ {
		var n int
		_, n, ok = dnsName(b, off)
		if !ok || n+10 > len(b) {
			break
		}
		typ := binary.BigEndian.Uint16(b[n:])
		ttl := binary.BigEndian.Uint32(b[n+4:])
		rdl := int(binary.BigEndian.Uint16(b[n+8:]))
		rd := n + 10
		if rd+rdl > len(b) {
			break
		}
		switch typ {
		case 1:
			if rdl == 4 {
				out = append(out, DNSAnswer{IP: netip.AddrFrom4([4]byte(b[rd : rd+4])), Name: qname, TTL: ttl})
			}
		case 28:
			if rdl == 16 {
				out = append(out, DNSAnswer{IP: netip.AddrFrom16([16]byte(b[rd : rd+16])), Name: qname, TTL: ttl})
			}
		case 5:
			if c, _, ok := dnsName(b, rd); ok {
				cname = c
			}
		}
		off = rd + rdl
	}
	for k := range out {
		out[k].CNAME = cname
	}
	return out
}

// dnsName reads a possibly compressed name at off; it returns the name and
// the offset after it.
func dnsName(b []byte, off int) (string, int, bool) {
	var sb strings.Builder
	end, jumps := -1, 0
	for {
		if off >= len(b) {
			return "", 0, false
		}
		l := int(b[off])
		switch {
		case l == 0:
			off++
			if end < 0 {
				end = off
			}
			return strings.ToLower(sb.String()), end, true
		case l&0xc0 == 0xc0:
			if off+1 >= len(b) || jumps > 16 {
				return "", 0, false
			}
			if end < 0 {
				end = off + 2
			}
			off = int(binary.BigEndian.Uint16(b[off:]) & 0x3fff)
			jumps++
		case l&0xc0 != 0:
			return "", 0, false
		default:
			if off+1+l > len(b) || sb.Len()+l > 253 {
				return "", 0, false
			}
			if sb.Len() > 0 {
				sb.WriteByte('.')
			}
			sb.Write(b[off+1 : off+1+l])
			off += 1 + l
		}
	}
}

// ClientHello reads the server name from a TLS ClientHello handshake message
// (type, 3-byte length, body). It works on a truncated message as long as
// the name comes before the cut. ech is set when the hello carries an
// encrypted ClientHello, whose outer name is only the provider's.
func ClientHello(h []byte) (sni string, ech bool) {
	if len(h) < 4 || h[0] != 1 {
		return "", false
	}
	b := h[4:]
	if l := int(h[1])<<16 | int(h[2])<<8 | int(h[3]); l < len(b) {
		b = b[:l]
	}
	p := 34 // version + random
	if p >= len(b) {
		return "", false
	}
	p += 1 + int(b[p]) // session id
	if p+2 > len(b) {
		return "", false
	}
	p += 2 + int(binary.BigEndian.Uint16(b[p:])) // cipher suites
	if p >= len(b) {
		return "", false
	}
	p += 1 + int(b[p]) // compression
	if p+2 > len(b) {
		return "", false
	}
	p += 2
	for p+4 <= len(b) {
		typ := binary.BigEndian.Uint16(b[p:])
		l := int(binary.BigEndian.Uint16(b[p+2:]))
		d := b[p+4:]
		if l < len(d) {
			d = d[:l]
		}
		switch typ {
		case 0x0000:
			if len(d) >= 5 && d[2] == 0 {
				nl := int(binary.BigEndian.Uint16(d[3:]))
				if 5+nl <= len(d) {
					sni = strings.ToLower(string(d[5 : 5+nl]))
				}
			}
		case 0xfe0d:
			ech = true
		}
		p += 4 + l
	}
	return sni, ech
}

// TLSRecordHello unwraps the first TLS record of a TCP stream.
func TLSRecordHello(payload []byte) (string, bool) {
	if len(payload) < 9 || payload[0] != 0x16 || payload[1] != 3 {
		return "", false
	}
	return ClientHello(payload[5:])
}
