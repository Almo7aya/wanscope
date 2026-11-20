package store

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// Files are a sequence of frames: uvarint length, then a payload whose first
// byte is its kind. 'K' frames add a string to the file's dictionary; 'B'
// frames hold a bucket whose keys refer to it. Buckets with the same start
// are summed when read, so a partial bucket written at shutdown and the rest
// written later add up.

type fileWriter struct {
	path string
	f    *os.File
	dict map[string]uint64
}

func openWriter(path string) (*fileWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	w := &fileWriter{path: path, dict: map[string]uint64{}}
	end, err := scanFile(path, func(id uint64, s string) { w.dict[s] = id }, nil)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	// drop a torn frame left by a power cut
	if err := f.Truncate(end); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.Seek(end, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	w.f = f
	return w, nil
}

func (w *fileWriter) Close() error { return w.f.Close() }

func (w *fileWriter) id(s string, out []byte) (uint64, []byte) {
	if id, ok := w.dict[s]; ok {
		return id, out
	}
	id := uint64(len(w.dict) + 1)
	w.dict[s] = id
	p := []byte{'K'}
	p = binary.AppendUvarint(p, id)
	p = append(p, s...)
	out = binary.AppendUvarint(out, uint64(len(p)))
	return id, append(out, p...)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// write appends buckets as one write.
func (w *fileWriter) write(buckets ...*Bucket) error {
	var out []byte
	for _, b := range buckets {
		p := []byte{'B'}
		p = binary.AppendVarint(p, b.T)
		p = binary.AppendUvarint(p, uint64(len(b.Series)))
		for _, k := range sortedKeys(b.Series) {
			var id uint64
			id, out = w.id(k, out)
			c := b.Series[k]
			p = binary.AppendUvarint(p, id)
			p = binary.AppendUvarint(p, c.Up)
			p = binary.AppendUvarint(p, c.Down)
		}
		p = binary.AppendUvarint(p, uint64(len(b.Labels)))
		for _, g := range sortedKeys(b.Labels) {
			var gid uint64
			gid, out = w.id(g, out)
			labels := b.Labels[g]
			p = binary.AppendUvarint(p, gid)
			p = binary.AppendUvarint(p, uint64(len(labels)))
			for _, l := range sortedKeys(labels) {
				var lid uint64
				lid, out = w.id(l, out)
				c := labels[l]
				p = binary.AppendUvarint(p, lid)
				p = binary.AppendUvarint(p, c.Up)
				p = binary.AppendUvarint(p, c.Down)
			}
		}
		out = binary.AppendUvarint(out, uint64(len(p)))
		out = append(out, p...)
	}
	_, err := w.f.Write(out)
	return err
}

var errFrame = errors.New("bad frame")

// scanFile reads a file's frames, calling dict for strings and bucket for
// buckets. It returns the offset after the last complete frame.
func scanFile(path string, dict func(uint64, string), bucket func(*Bucket)) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64*1024)
	names := map[uint64]string{}
	var off int64
	buf := make([]byte, 0, 4096)
	for {
		l, err := binary.ReadUvarint(r)
		if err != nil {
			return off, nil
		}
		if l == 0 || l > 64<<20 {
			return off, nil
		}
		if cap(buf) < int(l) {
			buf = make([]byte, l)
		}
		buf = buf[:l]
		if _, err := io.ReadFull(r, buf); err != nil {
			return off, nil
		}
		switch buf[0] {
		case 'K':
			id, n := binary.Uvarint(buf[1:])
			if n <= 0 {
				return off, nil
			}
			s := string(buf[1+n:])
			names[id] = s
			if dict != nil {
				dict(id, s)
			}
		case 'B':
			if bucket != nil {
				b, err := decodeBucket(buf[1:], names)
				if err != nil {
					return off, nil
				}
				bucket(b)
			}
		}
		off += int64(uvarintLen(l)) + int64(l)
	}
}

func uvarintLen(v uint64) int {
	n := 1
	for v >= 0x80 {
		v >>= 7
		n++
	}
	return n
}

func decodeBucket(p []byte, names map[uint64]string) (*Bucket, error) {
	rd := func() (uint64, bool) {
		v, n := binary.Uvarint(p)
		if n <= 0 {
			return 0, false
		}
		p = p[n:]
		return v, true
	}
	t, n := binary.Varint(p)
	if n <= 0 {
		return nil, errFrame
	}
	p = p[n:]
	b := &Bucket{T: t, Series: map[string]Counter{}}
	ns, ok := rd()
	if !ok {
		return nil, errFrame
	}
	for i := uint64(0); i < ns; i++ {
		id, ok1 := rd()
		up, ok2 := rd()
		down, ok3 := rd()
		if !ok1 || !ok2 || !ok3 {
			return nil, errFrame
		}
		k := intern(names[id])
		c := b.Series[k]
		c.Up += up
		c.Down += down
		b.Series[k] = c
	}
	ng, ok := rd()
	if !ok {
		return b, nil
	}
	if ng > 0 {
		b.Labels = make(map[string]map[string]Counter, ng)
	}
	for i := uint64(0); i < ng; i++ {
		gid, ok1 := rd()
		nl, ok2 := rd()
		if !ok1 || !ok2 {
			return nil, errFrame
		}
		g := intern(names[gid])
		m := b.Labels[g]
		if m == nil {
			m = make(map[string]Counter, nl)
			b.Labels[g] = m
		}
		for j := uint64(0); j < nl; j++ {
			lid, ok1 := rd()
			up, ok2 := rd()
			down, ok3 := rd()
			if !ok1 || !ok2 || !ok3 {
				return nil, errFrame
			}
			l := intern(names[lid])
			c := m[l]
			c.Up += up
			c.Down += down
			m[l] = c
		}
	}
	return b, nil
}

// readBuckets returns a file's buckets in order.
func readBuckets(path string) ([]*Bucket, error) {
	var out []*Bucket
	_, err := scanFile(path, nil, func(b *Bucket) { out = append(out, b) })
	if os.IsNotExist(err) {
		return nil, nil
	}
	return out, err
}
