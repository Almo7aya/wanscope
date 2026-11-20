package names

import (
	"encoding/binary"
	"fmt"
	"net/netip"

	"golang.org/x/net/bpf"
)

// The kernel hands us only what the filter accepts: DNS answers (UDP source
// port 53), TLS records starting a handshake on TCP 443 (a ClientHello), and
// QUIC long-header packets to UDP 443. Bulk traffic never reaches user space.
type ins struct {
	i      bpf.Instruction
	label  string // this instruction's label
	jt, jf string // jump targets for conditional jumps ("" = next)
	ja     string // unconditional jump target
}

func filterProgram() ([]bpf.RawInstruction, error) {
	ld := func(off, size uint32) bpf.Instruction { return bpf.LoadAbsolute{Off: off, Size: int(size)} }
	ldx := func(off, size uint32) bpf.Instruction { return bpf.LoadIndirect{Off: off, Size: int(size)} }
	jeq := func(v uint32, jt, jf string) ins {
		return ins{i: bpf.JumpIf{Cond: bpf.JumpEqual, Val: v}, jt: jt, jf: jf}
	}
	jset := func(v uint32, jt, jf string) ins {
		return ins{i: bpf.JumpIf{Cond: bpf.JumpBitsSet, Val: v}, jt: jt, jf: jf}
	}
	p := []ins{
		{i: ld(12, 2)},
		jeq(0x0800, "v4", ""),
		jeq(0x86dd, "v6", "drop"),

		{label: "v4", i: ld(20, 2)},
		jset(0x1fff, "drop", ""), // later fragments
		{i: ld(23, 1)},
		jeq(17, "v4udp", ""),
		jeq(6, "v4tcp", "drop"),

		{label: "v4udp", i: bpf.LoadMemShift{Off: 14}},
		{i: ldx(14, 2)},
		jeq(53, "accept", ""),
		{i: ldx(16, 2)},
		jeq(443, "", "drop"),
		{i: ldx(22, 1)},
		jset(0x80, "accept", "drop"),

		{label: "v4tcp", i: bpf.LoadMemShift{Off: 14}},
		{i: ldx(16, 2)},
		jeq(443, "", "drop"),
		{i: ldx(26, 1)},
		{i: bpf.ALUOpConstant{Op: bpf.ALUOpAnd, Val: 0xf0}},
		{i: bpf.ALUOpConstant{Op: bpf.ALUOpShiftRight, Val: 2}},
		{i: bpf.ALUOpX{Op: bpf.ALUOpAdd}},
		{i: bpf.TAX{}},
		{i: ldx(14, 1)},
		jeq(0x16, "accept", "drop"),

		{label: "v6", i: ld(20, 1)},
		jeq(17, "v6udp", ""),
		jeq(6, "v6tcp", "drop"),

		{label: "v6udp", i: ld(54, 2)},
		jeq(53, "accept", ""),
		{i: ld(56, 2)},
		jeq(443, "", "drop"),
		{i: ld(62, 1)},
		jset(0x80, "accept", "drop"),

		{label: "v6tcp", i: ld(56, 2)},
		jeq(443, "", "drop"),
		{i: ld(66, 1)},
		{i: bpf.ALUOpConstant{Op: bpf.ALUOpAnd, Val: 0xf0}},
		{i: bpf.ALUOpConstant{Op: bpf.ALUOpShiftRight, Val: 2}},
		{i: bpf.TAX{}},
		{i: ldx(54, 1)},
		jeq(0x16, "accept", "drop"),

		{label: "accept", i: bpf.RetConstant{Val: 0xffff}},
		{label: "drop", i: bpf.RetConstant{Val: 0}},
	}
	at := map[string]int{}
	for k, x := range p {
		if x.label != "" {
			at[x.label] = k
		}
	}
	skip := func(from int, label string) (uint8, error) {
		if label == "" {
			return 0, nil
		}
		to, ok := at[label]
		if !ok || to <= from || to-from-1 > 255 {
			return 0, fmt.Errorf("bad jump %d -> %s", from, label)
		}
		return uint8(to - from - 1), nil
	}
	out := make([]bpf.Instruction, len(p))
	for k, x := range p {
		switch j := x.i.(type) {
		case bpf.JumpIf:
			var err error
			if j.SkipTrue, err = skip(k, x.jt); err != nil {
				return nil, err
			}
			if j.SkipFalse, err = skip(k, x.jf); err != nil {
				return nil, err
			}
			out[k] = j
		default:
			out[k] = x.i
		}
	}
	return bpf.Assemble(out)
}

// Packet is what the sniffer hands on: addresses, ports, payload.
type Packet struct {
	Proto    uint8
	Src, Dst netip.AddrPort
	Payload  []byte
}

// parseFrame decodes an Ethernet frame down to the transport payload.
func parseFrame(b []byte) (*Packet, bool) {
	if len(b) < 14 {
		return nil, false
	}
	et := binary.BigEndian.Uint16(b[12:])
	b = b[14:]
	p := &Packet{}
	var src, dst netip.Addr
	switch et {
	case 0x0800:
		if len(b) < 20 || b[0]>>4 != 4 {
			return nil, false
		}
		ihl := int(b[0]&0x0f) * 4
		tot := int(binary.BigEndian.Uint16(b[2:]))
		if ihl < 20 || tot < ihl || len(b) < ihl {
			return nil, false
		}
		if tot < len(b) {
			b = b[:tot]
		}
		p.Proto = b[9]
		src = netip.AddrFrom4([4]byte(b[12:16]))
		dst = netip.AddrFrom4([4]byte(b[16:20]))
		b = b[ihl:]
	case 0x86dd:
		if len(b) < 40 {
			return nil, false
		}
		pl := int(binary.BigEndian.Uint16(b[4:]))
		p.Proto = b[6]
		src = netip.AddrFrom16([16]byte(b[8:24]))
		dst = netip.AddrFrom16([16]byte(b[24:40]))
		b = b[40:]
		if pl < len(b) {
			b = b[:pl]
		}
	default:
		return nil, false
	}
	switch p.Proto {
	case 17:
		if len(b) < 8 {
			return nil, false
		}
		p.Src = netip.AddrPortFrom(src, binary.BigEndian.Uint16(b))
		p.Dst = netip.AddrPortFrom(dst, binary.BigEndian.Uint16(b[2:]))
		p.Payload = b[8:]
	case 6:
		if len(b) < 20 {
			return nil, false
		}
		off := int(b[12]>>4) * 4
		if off < 20 || off > len(b) {
			return nil, false
		}
		p.Src = netip.AddrPortFrom(src, binary.BigEndian.Uint16(b))
		p.Dst = netip.AddrPortFrom(dst, binary.BigEndian.Uint16(b[2:]))
		p.Payload = b[off:]
	default:
		return nil, false
	}
	return p, true
}
