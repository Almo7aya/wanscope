package names

import (
	"bufio"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"io"
	"net/netip"
	"os"
	"sort"
	"strings"
)

// The optional data package (wanscope-data, built by cmd/wanscope-data) puts two files in DataDir:
//   - services.txt: more services by domain, from v2fly/domain-list-community (MIT), in the built-in list's
//     "Name|kind|domains" form. The built-in list and your own rules win over it.
//   - owners.bin.gz: who announces each IPv4 range, from DB-IP's ASN Lite (CC BY 4.0, https://db-ip.com), to name
//     connections nothing else names ("Amazon.com network").
const DataDir = "/usr/share/wanscope"

// LoadServices reads a services file: "Name|kind|domain domain ..." lines; # starts a comment.
func LoadServices(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' || strings.Count(line, "|") != 2 {
			continue
		}
		out = append(out, line)
	}
	return out, sc.Err()
}

// Owners names who announces an IPv4 address.
type Owners struct {
	Built  int64 // when the table was made (Unix seconds)
	names  []string
	starts []uint32 // range starts, sorted; a range runs to the next start
	owner  []uint32 // index into names + 1; 0: nobody (a gap)
}

// LoadOwners reads an owners.bin.gz file.
func LoadOwners(path string) (*Owners, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	r := bufio.NewReaderSize(zr, 256*1024)
	magic := make([]byte, 7)
	if _, err := io.ReadFull(r, magic); err != nil || string(magic) != "WSOWN1\n" {
		return nil, errors.New("not a wanscope owners file")
	}
	var hdr [8]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	le := binary.LittleEndian
	o := &Owners{Built: int64(le.Uint32(hdr[:4]))}
	n := le.Uint32(hdr[4:])
	if n > 10_000_000 {
		return nil, errors.New("owners file: too many names")
	}
	o.names = make([]string, n)
	buf := make([]byte, 255)
	for i := range o.names {
		l, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		if _, err := io.ReadFull(r, buf[:l]); err != nil {
			return nil, err
		}
		o.names[i] = string(buf[:l])
	}
	var cnt [4]byte
	if _, err := io.ReadFull(r, cnt[:]); err != nil {
		return nil, err
	}
	m := le.Uint32(cnt[:])
	if m > 20_000_000 {
		return nil, errors.New("owners file: too many ranges")
	}
	raw := make([]byte, 8*int(m))
	if _, err := io.ReadFull(r, raw); err != nil {
		return nil, err
	}
	o.starts, o.owner = make([]uint32, m), make([]uint32, m)
	for i := range o.starts {
		o.starts[i] = le.Uint32(raw[4*i:])
		o.owner[i] = le.Uint32(raw[4*int(m)+4*i:])
		if o.owner[i] > n {
			return nil, errors.New("owners file: bad name index")
		}
	}
	return o, nil
}

// Lookup is who announces a (public IPv4) address; "" when nobody does or o is nil.
func (o *Owners) Lookup(a netip.Addr) string {
	if o == nil {
		return ""
	}
	a = a.Unmap()
	if !a.Is4() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsMulticast() {
		return ""
	}
	b := a.As4()
	ip := binary.BigEndian.Uint32(b[:])
	i := sort.Search(len(o.starts), func(i int) bool { return o.starts[i] > ip }) - 1
	if i < 0 || o.owner[i] == 0 {
		return ""
	}
	return o.names[o.owner[i]-1]
}

// Len is how many ranges the table holds.
func (o *Owners) Len() int {
	if o == nil {
		return 0
	}
	return len(o.starts)
}
