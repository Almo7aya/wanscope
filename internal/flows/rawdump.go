package flows

import (
	"encoding/binary"
	"errors"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

// rawCT dumps the conntrack table with plain syscalls and one large buffer:
// fewer round trips than a general netlink library.
type rawCT struct {
	fd  int
	seq atomic.Uint32
	buf []byte
}

func openRawCT() (*rawCT, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_NETFILTER)
	if err != nil {
		return nil, err
	}
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		unix.Close(fd)
		return nil, err
	}
	unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUF, 4<<20)
	unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Sec: 5})
	return &rawCT{fd: fd, buf: make([]byte, 256<<10)}, nil
}

func (r *rawCT) Close() error { return unix.Close(r.fd) }

func (r *rawCT) dump() ([]Flow, error) {
	seq := r.seq.Add(1)
	req := make([]byte, 20)
	binary.NativeEndian.PutUint32(req[0:], 20)
	binary.NativeEndian.PutUint16(req[4:], nfnlSubsysCT<<8|ctMsgGet)
	binary.NativeEndian.PutUint16(req[6:], unix.NLM_F_REQUEST|unix.NLM_F_DUMP)
	binary.NativeEndian.PutUint32(req[8:], seq)
	req[16] = unix.AF_UNSPEC
	if err := unix.Sendto(r.fd, req, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return nil, err
	}
	out := make([]Flow, 0, 256)
	for {
		n, _, err := unix.Recvfrom(r.fd, r.buf, 0)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			return nil, err
		}
		b := r.buf[:n]
		for len(b) >= 16 {
			l := int(binary.NativeEndian.Uint32(b))
			typ := binary.NativeEndian.Uint16(b[4:])
			mseq := binary.NativeEndian.Uint32(b[8:])
			if l < 16 || l > len(b) {
				return out, errors.New("conntrack: bad netlink message")
			}
			if mseq == seq {
				switch typ {
				case unix.NLMSG_DONE:
					return out, nil
				case unix.NLMSG_ERROR:
					if l >= 20 {
						if code := int32(binary.NativeEndian.Uint32(b[16:])); code != 0 {
							return out, unix.Errno(-code)
						}
					}
					return out, nil
				default:
					if f, ok := parseFlow(b[16:l]); ok {
						out = append(out, f)
					}
				}
			}
			b = b[(l+3)&^3:]
		}
	}
}
