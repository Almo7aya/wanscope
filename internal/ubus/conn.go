package ubus

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"
)

const DefaultSocket = "/var/run/ubus/ubus.sock"

const (
	msgHello     = 0
	msgStatus    = 1
	msgData      = 2
	msgLookup    = 4
	msgInvoke    = 5
	msgAddObject = 6
)

const (
	attrStatus    = 1
	attrObjPath   = 2
	attrObjID     = 3
	attrMethod    = 4
	attrSignature = 6
	attrData      = 7
	attrNoReply   = 10
	attrUser      = 12
	attrGroup     = 13
)

// Status codes, as in ubusmsg.h.
const (
	StatusOK               = 0
	StatusInvalidArgument  = 2
	StatusMethodNotFound   = 3
	StatusNotFound         = 4
	StatusNoData           = 5
	StatusPermissionDenied = 6
	StatusTimeout          = 7
	StatusNotSupported     = 8
	StatusUnknownError     = 9
)

const maxMsg = 1 << 20 // UBUS_MAX_MSGLEN

const systemObjectEvent = 1

// Argument types for method signatures (shown by `ubus -v list`).
const (
	ArgString = typeString
	ArgInt    = typeInt32
	ArgBool   = typeInt8
	ArgTable  = typeTable
	ArgArray  = typeArray
)

type Arg struct {
	Name string
	Type int
}

// Error carries a ubus status code back to the caller.
type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string { return fmt.Sprintf("ubus status %d: %s", e.Status, e.Msg) }

// Errorf returns an invalid-argument error.
func Errorf(format string, a ...any) error {
	return &Error{Status: StatusInvalidArgument, Msg: fmt.Sprintf(format, a...)}
}

type Request struct {
	Method string
	Args   map[string]any
	User   string
	Group  string
}

// Decode fills v (a struct with json tags) from the request arguments.
func (r *Request) Decode(v any) error {
	raw, err := json.Marshal(r.Args)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return Errorf("bad arguments: %v", err)
	}
	return nil
}

// A Handler returns any JSON-marshalable object (encoded as the reply table)
// or nil for no data.
type Handler func(r *Request) (any, error)

type Method struct {
	Name    string
	Args    []Arg
	Handler Handler
}

type Object struct {
	Name    string
	Methods []Method
	id      uint32
}

type header struct {
	typ  uint8
	seq  uint16
	peer uint32
}

type reply struct {
	data   [][]byte // DATA payloads (blobmsg tables)
	status int
	done   chan struct{}
}

type conn struct {
	sock    net.Conn
	r       *bufio.Reader
	wmu     sync.Mutex
	mu      sync.Mutex
	seq     uint16
	waits   map[uint16]*reply
	objects map[uint32]*Object
	lookups map[string]uint32
}

// Bus keeps a connection to ubusd, re-registering its objects after ubusd
// restarts.
type Bus struct {
	path    string
	objects []*Object
	log     *slog.Logger

	mu  sync.Mutex
	cur *conn
}

func NewBus(path string, log *slog.Logger, objects ...*Object) *Bus {
	if path == "" {
		path = DefaultSocket
	}
	return &Bus{path: path, objects: objects, log: log}
}

// Run connects and serves until ctx ends.
func (b *Bus) Run(ctx context.Context) {
	for ctx.Err() == nil {
		c, err := dial(b.path)
		if err == nil {
			for _, o := range b.objects {
				if err = c.addObject(o); err != nil {
					break
				}
			}
		}
		if err != nil {
			b.log.Warn("ubus connect", "err", err)
			if c != nil {
				c.sock.Close()
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
			continue
		}
		b.mu.Lock()
		b.cur = c
		b.mu.Unlock()
		stop := context.AfterFunc(ctx, func() { c.sock.Close() })
		err = c.readLoop()
		stop()
		b.mu.Lock()
		b.cur = nil
		b.mu.Unlock()
		c.failAll()
		if ctx.Err() == nil {
			b.log.Warn("ubus connection lost", "err", err)
			time.Sleep(time.Second)
		}
	}
}

func (b *Bus) conn() (*conn, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cur == nil {
		return nil, errors.New("ubus: not connected")
	}
	return b.cur, nil
}

// Call invokes path.method with args and decodes the reply into out (if not
// nil). The bus must be running.
func (b *Bus) Call(path, method string, args any, out any) error {
	c, err := b.conn()
	if err != nil {
		return err
	}
	return c.call(path, method, args, out)
}

// Send broadcasts a ubus event (like `ubus send id data`).
func (b *Bus) Send(id string, data any) error {
	c, err := b.conn()
	if err != nil {
		return err
	}
	payload, err := encodeTable(data)
	if err != nil {
		return err
	}
	var inner writer
	off := inner.msgOpen("id")
	inner.b = append(inner.b, id...)
	inner.b = append(inner.b, 0)
	inner.close(off, typeString, true)
	off = inner.msgOpen("data")
	inner.b = append(inner.b, payload...)
	inner.close(off, typeTable, true)

	var w writer
	w.u32(attrObjID, systemObjectEvent)
	w.str(attrMethod, "send")
	off = w.open()
	w.b = append(w.b, inner.b...)
	w.close(off, attrData, false)
	_, err = c.request(msgInvoke, systemObjectEvent, w.b)
	return err
}

func dial(path string) (*conn, error) {
	s, err := net.DialTimeout("unix", path, 5*time.Second)
	if err != nil {
		return nil, err
	}
	c := &conn{sock: s, r: bufio.NewReader(s), waits: map[uint16]*reply{}, objects: map[uint32]*Object{}, lookups: map[string]uint32{}}
	s.SetReadDeadline(time.Now().Add(5 * time.Second))
	h, _, err := readMsg(c.r)
	s.SetReadDeadline(time.Time{})
	if err != nil {
		s.Close()
		return nil, err
	}
	if h.typ != msgHello {
		s.Close()
		return nil, fmt.Errorf("ubus: expected hello, got %d", h.typ)
	}
	return c, nil
}

// readMsg reads one message: header, then the blob holding its attributes.
// It returns the attributes' payload.
func readMsg(r *bufio.Reader) (header, []byte, error) {
	var hb [12]byte
	if _, err := io.ReadFull(r, hb[:]); err != nil {
		return header{}, nil, err
	}
	h := header{typ: hb[1], seq: binary.BigEndian.Uint16(hb[2:]), peer: binary.BigEndian.Uint32(hb[4:])}
	if hb[0] != 0 {
		return h, nil, fmt.Errorf("ubus: bad version %d", hb[0])
	}
	l := int(binary.BigEndian.Uint32(hb[8:]) & attrLenMask)
	if l < 4 || l > maxMsg {
		return h, nil, fmt.Errorf("ubus: bad length %d", l)
	}
	body := make([]byte, l-4)
	if _, err := io.ReadFull(r, body); err != nil {
		return h, nil, err
	}
	return h, body, nil
}

// send writes one message whose attributes are attrs.
func (c *conn) send(typ uint8, seq uint16, peer uint32, attrs []byte) error {
	if len(attrs)+4 > maxMsg {
		return &Error{Status: StatusUnknownError, Msg: "reply too large"}
	}
	msg := make([]byte, 12, 12+len(attrs))
	msg[1] = typ
	binary.BigEndian.PutUint16(msg[2:], seq)
	binary.BigEndian.PutUint32(msg[4:], peer)
	binary.BigEndian.PutUint32(msg[8:], uint32(4+len(attrs)))
	msg = append(msg, attrs...)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.sock.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err := c.sock.Write(msg)
	return err
}

// request sends a message and waits for its STATUS, collecting DATA replies.
func (c *conn) request(typ uint8, peer uint32, attrs []byte) (*reply, error) {
	c.mu.Lock()
	c.seq++
	seq := c.seq
	r := &reply{status: -1, done: make(chan struct{})}
	c.waits[seq] = r
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.waits, seq)
		c.mu.Unlock()
	}()
	if err := c.send(typ, seq, peer, attrs); err != nil {
		return nil, err
	}
	select {
	case <-r.done:
	case <-time.After(15 * time.Second):
		return nil, &Error{Status: StatusTimeout, Msg: "no reply"}
	}
	if r.status != StatusOK {
		return r, &Error{Status: r.status, Msg: "request failed"}
	}
	return r, nil
}

func (c *conn) failAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for seq, r := range c.waits {
		r.status = StatusUnknownError
		close(r.done)
		delete(c.waits, seq)
	}
}

func (c *conn) addObject(o *Object) error {
	var sig writer
	for _, m := range o.Methods {
		off := sig.msgOpen(m.Name)
		for _, a := range m.Args {
			ao := sig.msgOpen(a.Name)
			sig.b = binary.BigEndian.AppendUint32(sig.b, uint32(a.Type))
			sig.close(ao, typeInt32, true)
		}
		sig.close(off, typeTable, true)
	}
	var w writer
	w.str(attrObjPath, o.Name)
	off := w.open()
	w.b = append(w.b, sig.b...)
	w.close(off, attrSignature, false)

	// The read loop isn't running yet: read the reply here.
	c.mu.Lock()
	c.seq++
	seq := c.seq
	r := &reply{status: -1, done: make(chan struct{})}
	c.waits[seq] = r
	c.mu.Unlock()
	if err := c.send(msgAddObject, seq, 0, w.b); err != nil {
		return err
	}
	c.sock.SetReadDeadline(time.Now().Add(15 * time.Second))
	defer c.sock.SetReadDeadline(time.Time{})
	for {
		select {
		case <-r.done:
			if r.status != StatusOK {
				return &Error{Status: r.status, Msg: "add object " + o.Name}
			}
			for _, d := range r.data {
				if id, ok := u32Attr(d, attrObjID); ok {
					o.id = id
				}
			}
			if o.id == 0 {
				return errors.New("ubus: no object id")
			}
			c.mu.Lock()
			c.objects[o.id] = o
			c.mu.Unlock()
			return nil
		default:
		}
		h, body, err := readMsg(c.r)
		if err != nil {
			return err
		}
		c.dispatch(h, body)
	}
}

// u32Attr finds an int32 message attribute in a raw attribute list.
func u32Attr(raw []byte, id int) (uint32, bool) {
	attrs, err := parseAttrs(raw)
	if err != nil {
		return 0, false
	}
	for _, a := range attrs {
		if a.id == id && len(a.data) >= 4 {
			return binary.BigEndian.Uint32(a.data), true
		}
	}
	return 0, false
}

func (c *conn) readLoop() error {
	for {
		h, body, err := readMsg(c.r)
		if err != nil {
			return err
		}
		c.dispatch(h, body)
	}
}

func (c *conn) dispatch(h header, body []byte) {
	switch h.typ {
	case msgData, msgStatus:
		c.mu.Lock()
		r := c.waits[h.seq]
		c.mu.Unlock()
		if r == nil {
			return
		}
		if h.typ == msgData {
			// keep the raw attributes; callers pick what they need
			r.data = append(r.data, body)
			return
		}
		if st, ok := u32Attr(body, attrStatus); ok {
			r.status = int(st)
		} else {
			r.status = StatusUnknownError
		}
		c.mu.Lock()
		if c.waits[h.seq] == r {
			delete(c.waits, h.seq)
			close(r.done)
		}
		c.mu.Unlock()
	case msgInvoke:
		go c.invoke(h, body)
	}
}

func (c *conn) invoke(h header, body []byte) {
	attrs, err := parseAttrs(body)
	if err != nil {
		return
	}
	var objID uint32
	var method string
	var data []byte
	noReply := false
	req := &Request{}
	for _, a := range attrs {
		switch a.id {
		case attrObjID:
			if len(a.data) >= 4 {
				objID = binary.BigEndian.Uint32(a.data)
			}
		case attrMethod:
			method = string(bytes.TrimRight(a.data, "\x00"))
		case attrData:
			data = a.data
		case attrNoReply:
			noReply = len(a.data) > 0 && a.data[0] != 0
		case attrUser:
			req.User = string(bytes.TrimRight(a.data, "\x00"))
		case attrGroup:
			req.Group = string(bytes.TrimRight(a.data, "\x00"))
		}
	}
	status := StatusOK
	var result any
	c.mu.Lock()
	obj := c.objects[objID]
	c.mu.Unlock()
	var m *Method
	if obj != nil {
		for i := range obj.Methods {
			if obj.Methods[i].Name == method {
				m = &obj.Methods[i]
			}
		}
	}
	switch {
	case obj == nil:
		status = StatusNotFound
	case m == nil:
		status = StatusMethodNotFound
	default:
		req.Method = method
		req.Args, err = decodeTable(data)
		if err != nil {
			status = StatusInvalidArgument
			break
		}
		result, err = safeCall(m.Handler, req)
		if err != nil {
			var ue *Error
			if errors.As(err, &ue) {
				status = ue.Status
			} else {
				status = StatusUnknownError
			}
			result = map[string]any{"error": err.Error()}
		}
	}
	if noReply {
		return
	}
	if result != nil {
		payload, err := encodeTable(result)
		if err != nil {
			status = StatusUnknownError
		} else {
			var w writer
			w.u32(attrObjID, objID)
			off := w.open()
			w.b = append(w.b, payload...)
			w.close(off, attrData, false)
			if err := c.send(msgData, h.seq, h.peer, w.b); err != nil {
				status = StatusUnknownError
			}
		}
	}
	var w writer
	w.u32(attrStatus, uint32(status))
	w.u32(attrObjID, objID)
	c.send(msgStatus, h.seq, h.peer, w.b)
}

func safeCall(fn Handler, r *Request) (res any, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = &Error{Status: StatusUnknownError, Msg: fmt.Sprint(p)}
		}
	}()
	return fn(r)
}

func (c *conn) lookup(path string) (uint32, error) {
	c.mu.Lock()
	id, ok := c.lookups[path]
	c.mu.Unlock()
	if ok {
		return id, nil
	}
	var w writer
	w.str(attrObjPath, path)
	r, err := c.request(msgLookup, 0, w.b)
	if err != nil {
		return 0, err
	}
	for _, d := range r.data {
		if id, ok := u32Attr(d, attrObjID); ok {
			c.mu.Lock()
			c.lookups[path] = id
			c.mu.Unlock()
			return id, nil
		}
	}
	return 0, &Error{Status: StatusNotFound, Msg: path}
}

func (c *conn) call(path, method string, args any, out any) error {
	id, err := c.lookup(path)
	if err != nil {
		return err
	}
	payload, err := encodeTable(args)
	if err != nil {
		return err
	}
	var w writer
	w.u32(attrObjID, id)
	w.str(attrMethod, method)
	off := w.open()
	w.b = append(w.b, payload...)
	w.close(off, attrData, false)
	r, err := c.request(msgInvoke, id, w.b)
	if err != nil {
		var ue *Error
		if errors.As(err, &ue) && ue.Status == StatusNotFound {
			c.mu.Lock()
			delete(c.lookups, path)
			c.mu.Unlock()
		}
		return err
	}
	if out == nil {
		return nil
	}
	merged := map[string]any{}
	for _, d := range r.data {
		attrs, err := parseAttrs(d)
		if err != nil {
			return err
		}
		for _, a := range attrs {
			if a.id != attrData {
				continue
			}
			m, err := decodeTable(a.data)
			if err != nil {
				return err
			}
			for k, v := range m {
				merged[k] = v
			}
		}
	}
	raw, err := json.Marshal(merged)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}
