package flows

import (
	"encoding/binary"
	"net/netip"

	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

// A small ctnetlink reader: only the attributes wanscope uses, parsed in
// place. Netlink headers are in host byte order, conntrack values in network
// byte order.

const (
	nfnlSubsysCT   = 1
	ctMsgNew       = 0
	ctMsgGet       = 1
	ctMsgDelete    = 2
	groupCTDestroy = 3 // NFNLGRP_CONNTRACK_DESTROY

	ctaTupleOrig     = 1
	ctaTupleReply    = 2
	ctaProtoinfo     = 4
	ctaMark          = 8
	ctaCountersOrig  = 9
	ctaCountersReply = 10
	ctaID            = 12

	ctaTupleIP    = 1
	ctaTupleProto = 2

	ctaIPv4Src = 1
	ctaIPv4Dst = 2
	ctaIPv6Src = 3
	ctaIPv6Dst = 4

	ctaProtoNum     = 1
	ctaProtoSrcPort = 2
	ctaProtoDstPort = 3

	ctaCountersBytes   = 2
	ctaCountersBytes32 = 4

	ctaProtoinfoTCP      = 1
	ctaProtoinfoTCPState = 1
)

func dialCT() (*netlink.Conn, error) {
	return netlink.Dial(unix.NETLINK_NETFILTER, nil)
}

// walk calls fn for each attribute (type without the nested/byte-order flags).
func walk(b []byte, fn func(typ uint16, v []byte)) {
	for len(b) >= 4 {
		l := int(binary.NativeEndian.Uint16(b))
		t := binary.NativeEndian.Uint16(b[2:]) & 0x3fff
		if l < 4 || l > len(b) {
			return
		}
		fn(t, b[4:l])
		l = (l + 3) &^ 3
		if l > len(b) {
			return
		}
		b = b[l:]
	}
}

type tuple struct {
	proto    uint8
	src, dst netip.AddrPort
}

func parseTuple(b []byte) tuple {
	var t tuple
	var src, dst netip.Addr
	var sp, dp uint16
	walk(b, func(typ uint16, v []byte) {
		switch typ {
		case ctaTupleIP:
			walk(v, func(typ uint16, v []byte) {
				switch {
				case typ == ctaIPv4Src && len(v) == 4:
					src = netip.AddrFrom4([4]byte(v))
				case typ == ctaIPv4Dst && len(v) == 4:
					dst = netip.AddrFrom4([4]byte(v))
				case typ == ctaIPv6Src && len(v) == 16:
					src = netip.AddrFrom16([16]byte(v)).Unmap()
				case typ == ctaIPv6Dst && len(v) == 16:
					dst = netip.AddrFrom16([16]byte(v)).Unmap()
				}
			})
		case ctaTupleProto:
			walk(v, func(typ uint16, v []byte) {
				switch {
				case typ == ctaProtoNum && len(v) >= 1:
					t.proto = v[0]
				case typ == ctaProtoSrcPort && len(v) >= 2:
					sp = binary.BigEndian.Uint16(v)
				case typ == ctaProtoDstPort && len(v) >= 2:
					dp = binary.BigEndian.Uint16(v)
				}
			})
		}
	})
	t.src, t.dst = netip.AddrPortFrom(src, sp), netip.AddrPortFrom(dst, dp)
	return t
}

func counterBytes(b []byte) uint64 {
	var n uint64
	walk(b, func(typ uint16, v []byte) {
		switch {
		case typ == ctaCountersBytes && len(v) == 8:
			n = binary.BigEndian.Uint64(v)
		case typ == ctaCountersBytes32 && len(v) == 4:
			n = uint64(binary.BigEndian.Uint32(v))
		}
	})
	return n
}

// parseFlow reads one conntrack message (after the netlink header).
func parseFlow(data []byte) (Flow, bool) {
	if len(data) < 4 {
		return Flow{}, false
	}
	var f Flow
	var o, r tuple
	walk(data[4:], func(typ uint16, v []byte) {
		switch typ {
		case ctaTupleOrig:
			o = parseTuple(v)
		case ctaTupleReply:
			r = parseTuple(v)
		case ctaMark:
			if len(v) == 4 {
				f.Mark = binary.BigEndian.Uint32(v)
			}
		case ctaID:
			if len(v) == 4 {
				f.ID = binary.BigEndian.Uint32(v)
			}
		case ctaCountersOrig:
			f.OrigBytes = counterBytes(v)
		case ctaCountersReply:
			f.ReplyBytes = counterBytes(v)
		case ctaProtoinfo:
			walk(v, func(typ uint16, v []byte) {
				if typ == ctaProtoinfoTCP {
					walk(v, func(typ uint16, v []byte) {
						if typ == ctaProtoinfoTCPState && len(v) >= 1 {
							f.TCPState = v[0]
						}
					})
				}
			})
		}
	})
	if !o.src.Addr().IsValid() {
		return Flow{}, false
	}
	f.Proto = o.proto
	f.Src, f.Dst = o.src, o.dst
	f.ReplySrc, f.ReplyDst = r.src, r.dst
	return f, true
}
