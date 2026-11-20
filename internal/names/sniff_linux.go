package names

import (
	"fmt"
	"log/slog"
	"net"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

type Sniffer struct {
	log     *slog.Logger
	handle  func(*Packet)
	stop    atomic.Bool
	Packets atomic.Uint64
}

func NewSniffer(log *slog.Logger, handle func(*Packet)) *Sniffer {
	return &Sniffer{log: log, handle: handle}
}

func htons(v uint16) uint16 { return v<<8 | v>>8 }

// Watch reads from one interface until Stop. It retries if the interface
// goes away.
func (s *Sniffer) Watch(dev string) {
	for !s.stop.Load() {
		err := s.watch(dev)
		if s.stop.Load() {
			return
		}
		s.log.Warn("sniffer", "dev", dev, "err", err)
		time.Sleep(10 * time.Second)
	}
}

func (s *Sniffer) Stop() { s.stop.Store(true) }

func (s *Sniffer) watch(dev string) error {
	ifc, err := net.InterfaceByName(dev)
	if err != nil {
		return err
	}
	prog, err := filterProgram()
	if err != nil {
		return err
	}
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_CLOEXEC, int(htons(unix.ETH_P_ALL)))
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	filt := make([]unix.SockFilter, len(prog))
	for k, r := range prog {
		filt[k] = unix.SockFilter{Code: r.Op, Jt: r.Jt, Jf: r.Jf, K: r.K}
	}
	fprog := unix.SockFprog{Len: uint16(len(filt)), Filter: (*unix.SockFilter)(unsafe.Pointer(&filt[0]))}
	if err := unix.SetsockoptSockFprog(fd, unix.SOL_SOCKET, unix.SO_ATTACH_FILTER, &fprog); err != nil {
		return fmt.Errorf("attach filter: %w", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrLinklayer{Protocol: htons(unix.ETH_P_ALL), Ifindex: ifc.Index}); err != nil {
		return err
	}
	unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUF, 2<<20)
	unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Sec: 1})
	buf := make([]byte, 65536)
	for !s.stop.Load() {
		n, _, err := unix.Recvfrom(fd, buf, 0)
		if err != nil {
			if err == unix.EAGAIN || err == unix.EINTR {
				continue
			}
			return err
		}
		if p, ok := parseFrame(buf[:n]); ok {
			s.Packets.Add(1)
			s.handle(p)
		}
	}
	return nil
}
