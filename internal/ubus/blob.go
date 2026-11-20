// Package ubus speaks OpenWrt's ubus wire protocol directly, so wanscope can
// publish its own object (LuCI calls it through rpcd) and call other objects
// without forking the ubus CLI.
//
// A message is an 8-byte header followed by one blob attribute whose payload
// holds the message attributes. Method arguments and replies are blobmsg
// tables: extended blob attributes carrying a name and a typed value.
package ubus

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
)

const (
	attrExtended = 0x80000000
	attrIDMask   = 0x7f000000
	attrIDShift  = 24
	attrLenMask  = 0x00ffffff
)

// blobmsg value types.
const (
	typeArray  = 1
	typeTable  = 2
	typeString = 3
	typeInt64  = 4
	typeInt32  = 5
	typeInt16  = 6
	typeInt8   = 7 // also bool
	typeDouble = 8
)

var errShort = errors.New("ubus: truncated attribute")

func pad4(n int) int { return (n + 3) &^ 3 }

type attr struct {
	id   int
	ext  bool
	data []byte // payload without the 4-byte header and padding
}

func parseAttrs(b []byte) ([]attr, error) {
	var out []attr
	for len(b) > 0 {
		if len(b) < 4 {
			return nil, errShort
		}
		h := binary.BigEndian.Uint32(b)
		l := int(h & attrLenMask)
		if l < 4 || l > len(b) {
			return nil, errShort
		}
		out = append(out, attr{id: int((h & attrIDMask) >> attrIDShift), ext: h&attrExtended != 0, data: b[4:l]})
		p := pad4(l)
		if p > len(b) {
			p = len(b)
		}
		b = b[p:]
	}
	return out, nil
}

// writer builds blob attributes. Its buffer always starts 4-aligned, so
// padding to the buffer length pads each attribute.
type writer struct{ b []byte }

func (w *writer) open() int {
	off := len(w.b)
	w.b = append(w.b, 0, 0, 0, 0)
	return off
}

// close sets the header of the attribute opened at off. A nested attribute's
// length includes its children's padding, as libubox does.
func (w *writer) close(off, id int, ext bool) {
	h := uint32(id)<<attrIDShift | uint32(len(w.b)-off)
	if ext {
		h |= attrExtended
	}
	binary.BigEndian.PutUint32(w.b[off:], h)
	for len(w.b)%4 != 0 {
		w.b = append(w.b, 0)
	}
}

func (w *writer) u32(id int, v uint32) {
	off := w.open()
	w.b = binary.BigEndian.AppendUint32(w.b, v)
	w.close(off, id, false)
}

func (w *writer) str(id int, s string) {
	off := w.open()
	w.b = append(w.b, s...)
	w.b = append(w.b, 0)
	w.close(off, id, false)
}

// msgOpen starts a blobmsg attribute: header, name length, name, NUL, padding.
func (w *writer) msgOpen(name string) int {
	off := w.open()
	w.b = binary.BigEndian.AppendUint16(w.b, uint16(len(name)))
	w.b = append(w.b, name...)
	w.b = append(w.b, 0)
	for len(w.b)%4 != 0 {
		w.b = append(w.b, 0)
	}
	return off
}

// value writes v (decoded JSON: map, slice, string, json.Number, bool, nil)
// as a blobmsg attribute named name. Nil values are skipped.
func (w *writer) value(name string, v any) {
	switch x := v.(type) {
	case nil:
		return
	case string:
		off := w.msgOpen(name)
		w.b = append(w.b, x...)
		w.b = append(w.b, 0)
		w.close(off, typeString, true)
	case bool:
		off := w.msgOpen(name)
		if x {
			w.b = append(w.b, 1)
		} else {
			w.b = append(w.b, 0)
		}
		w.close(off, typeInt8, true)
	case json.Number:
		if i, err := strconv.ParseInt(string(x), 10, 64); err == nil {
			off := w.msgOpen(name)
			w.b = binary.BigEndian.AppendUint64(w.b, uint64(i))
			w.close(off, typeInt64, true)
			return
		}
		f, _ := x.Float64()
		off := w.msgOpen(name)
		w.b = binary.BigEndian.AppendUint64(w.b, math.Float64bits(f))
		w.close(off, typeDouble, true)
	case map[string]any:
		off := w.msgOpen(name)
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			w.value(k, x[k])
		}
		w.close(off, typeTable, true)
	case []any:
		off := w.msgOpen(name)
		for _, e := range x {
			if e == nil {
				e = "" // arrays keep their positions
			}
			w.value("", e)
		}
		w.close(off, typeArray, true)
	default:
		panic(fmt.Sprintf("ubus: cannot encode %T", v))
	}
}

// encodeTable turns any JSON-marshalable value into the payload of a blobmsg
// table (its fields, without an enclosing attribute).
func encodeTable(v any) ([]byte, error) {
	var m map[string]any
	if v != nil {
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		if err := d.Decode(&m); err != nil {
			return nil, fmt.Errorf("ubus: reply must be an object: %w", err)
		}
	}
	var w writer
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		w.value(k, m[k])
	}
	return w.b, nil
}

// decodeTable reads blobmsg attributes into a map.
func decodeTable(b []byte) (map[string]any, error) {
	attrs, err := parseAttrs(b)
	if err != nil {
		return nil, err
	}
	m := make(map[string]any, len(attrs))
	for _, a := range attrs {
		name, v, err := decodeMsg(a)
		if err != nil {
			return nil, err
		}
		m[name] = v
	}
	return m, nil
}

func decodeMsg(a attr) (string, any, error) {
	if !a.ext || len(a.data) < 2 {
		return "", nil, errShort
	}
	nl := int(binary.BigEndian.Uint16(a.data))
	hl := pad4(2 + nl + 1)
	if hl > len(a.data) {
		return "", nil, errShort
	}
	name := string(a.data[2 : 2+nl])
	d := a.data[hl:]
	switch a.id {
	case typeString:
		return name, string(bytes.TrimRight(d, "\x00")), nil
	case typeInt64:
		if len(d) < 8 {
			return name, nil, errShort
		}
		return name, int64(binary.BigEndian.Uint64(d)), nil
	case typeInt32:
		if len(d) < 4 {
			return name, nil, errShort
		}
		return name, int64(int32(binary.BigEndian.Uint32(d))), nil
	case typeInt16:
		if len(d) < 2 {
			return name, nil, errShort
		}
		return name, int64(int16(binary.BigEndian.Uint16(d))), nil
	case typeInt8:
		if len(d) < 1 {
			return name, nil, errShort
		}
		return name, d[0] != 0, nil
	case typeDouble:
		if len(d) < 8 {
			return name, nil, errShort
		}
		return name, math.Float64frombits(binary.BigEndian.Uint64(d)), nil
	case typeTable:
		m, err := decodeTable(d)
		return name, m, err
	case typeArray:
		attrs, err := parseAttrs(d)
		if err != nil {
			return name, nil, err
		}
		arr := make([]any, 0, len(attrs))
		for _, e := range attrs {
			_, v, err := decodeMsg(e)
			if err != nil {
				return name, nil, err
			}
			arr = append(arr, v)
		}
		return name, arr, nil
	}
	return name, nil, nil
}
