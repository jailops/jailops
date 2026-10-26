package fsagent

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"

	"jailops.net/jailops/internal/repo/catalog"
)

// Client talks to an agent. A client of a read agent is a catalog.ScanFS, one of a write agent a
// catalog.Sink; the other methods answer with an error. Calls may be made concurrently on a read agent
// (the scan does); a write agent is used by one goroutine at a time.
type Client struct {
	conn net.Conn
	br   *bufio.Reader
	wmu  sync.Mutex

	mu    sync.Mutex
	next  uint32
	calls map[uint32]*call
	err   error
	done  chan struct{}
}

type call struct {
	res  chan result
	data chan []byte // the D frames of an open stream
	end  chan struct{}
}

type result struct {
	body []byte
	err  error
}

var (
	_ catalog.ScanFS = (*Client)(nil)
	_ catalog.Sink   = (*Client)(nil)
)

// NewClient starts using conn, which the client owns from here (Close closes it).
func NewClient(conn net.Conn) *Client {
	c := &Client{conn: conn, br: bufio.NewReaderSize(conn, 256<<10), calls: map[uint32]*call{}, done: make(chan struct{})}
	go c.readLoop()
	return c
}

// Close ends the session; the agent exits when it sees the connection close.
func (c *Client) Close() error {
	c.fail(io.ErrClosedPipe)
	return c.conn.Close()
}

func (c *Client) fail(err error) {
	c.mu.Lock()
	if c.err == nil {
		c.err = err
		close(c.done)
	}
	for id, cl := range c.calls {
		select {
		case cl.res <- result{err: err}:
		default:
		}
		delete(c.calls, id)
	}
	c.mu.Unlock()
}

func (c *Client) readLoop() {
	for {
		f, err := readFrame(c.br)
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			c.fail(fmt.Errorf("fsagent: the agent connection ended: %w", err))
			return
		}
		c.mu.Lock()
		cl := c.calls[f.id]
		c.mu.Unlock()
		if cl == nil {
			continue // a reply to something abandoned
		}
		switch f.kind {
		case kindReply:
			cl.res <- result{body: f.body}
		case kindError:
			cl.finish(result{err: decodeErr(f.body, catalog.ErrSocket)})
			c.forget(f.id)
		case kindData:
			if cl.data != nil {
				cl.data <- f.body
			}
		case kindEnd:
			if cl.end != nil {
				close(cl.end)
			}
			c.forget(f.id)
		}
	}
}

// finish delivers an error to whoever waits on the call's result.
func (cl *call) finish(r result) {
	select {
	case cl.res <- r:
	default:
	}
}

func (c *Client) forget(id uint32) {
	c.mu.Lock()
	delete(c.calls, id)
	c.mu.Unlock()
}

func (c *Client) send(f frame) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return writeFrame(c.conn, f)
}

func (c *Client) register(cl *call) (uint32, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return 0, c.err
	}
	c.next++
	id := c.next
	c.calls[id] = cl
	return id, nil
}

// do sends a request and waits for its reply body.
func (c *Client) do(req request) ([]byte, error) {
	cl := &call{res: make(chan result, 1)}
	id, err := c.register(cl)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(req)
	if err := c.send(frame{kind: kindQuery, id: id, body: body}); err != nil {
		c.forget(id)
		return nil, err
	}
	r := <-cl.res
	c.forget(id)
	return r.body, r.err
}

// ---- catalog.ScanFS ----

// StatEntry implements catalog.ScanFS.
func (c *Client) StatEntry(path, name string) (*catalog.Entry, uint64, uint64, error) {
	b, err := c.do(request{Op: opStat, Path: path, Name: name})
	if err != nil {
		return nil, 0, 0, err
	}
	var r statReply
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, 0, 0, err
	}
	var e catalog.Entry
	if err := json.Unmarshal(r.Entry, &e); err != nil {
		return nil, 0, 0, err
	}
	return &e, r.Nlink, r.Dev, nil
}

// ReadNames implements catalog.ScanFS.
func (c *Client) ReadNames(path string) ([]string, error) {
	b, err := c.do(request{Op: opNames, Path: path})
	if err != nil {
		return nil, err
	}
	var r namesReply
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return r.Names, nil
}

// Open implements catalog.ScanFS.
func (c *Client) Open(path string) (io.ReadCloser, error) {
	cl := &call{res: make(chan result, 1), data: make(chan []byte, DefaultWindow/ChunkSize+8), end: make(chan struct{})}
	id, err := c.register(cl)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(request{Op: opOpen, Path: path, Window: DefaultWindow})
	if err := c.send(frame{kind: kindQuery, id: id, body: body}); err != nil {
		c.forget(id)
		return nil, err
	}
	r := <-cl.res
	if r.err != nil {
		c.forget(id)
		return nil, r.err
	}
	return &fileReader{c: c, id: id, cl: cl}, nil
}

type fileReader struct {
	c        *Client
	id       uint32
	cl       *call
	cur      []byte
	consumed int
	eof      bool
	err      error
}

func (f *fileReader) Read(p []byte) (int, error) {
	for len(f.cur) == 0 {
		if f.err != nil {
			return 0, f.err
		}
		if f.eof {
			return 0, io.EOF
		}
		select {
		case b := <-f.cl.data:
			f.cur = b
		case <-f.cl.end:
			// everything sent before E was queued before it
			select {
			case b := <-f.cl.data:
				f.cur = b
			default:
				f.eof = true
			}
		case r := <-f.cl.res:
			f.err = r.err
			if f.err == nil {
				f.err = errors.New("fsagent: unexpected reply inside a file")
			}
		case <-f.c.done:
			f.c.mu.Lock()
			f.err = f.c.err
			f.c.mu.Unlock()
		}
	}
	n := copy(p, f.cur)
	f.cur = f.cur[n:]
	f.consumed += n
	if f.consumed >= DefaultWindow/4 && !f.eof {
		var b [4]byte
		b[0], b[1], b[2], b[3] = byte(f.consumed>>24), byte(f.consumed>>16), byte(f.consumed>>8), byte(f.consumed)
		_ = f.c.send(frame{kind: kindCredit, id: f.id, body: b[:]})
		f.consumed = 0
	}
	return n, nil
}

func (f *fileReader) Close() error {
	if !f.eof && f.err == nil {
		_ = f.c.send(frame{kind: kindAbort, id: f.id})
	}
	f.c.forget(f.id)
	return nil
}

// ---- catalog.Sink ----

// Begin implements catalog.Sink.
func (c *Client) Begin() error { _, err := c.do(request{Op: opBegin}); return err }

// Mkdir implements catalog.Sink.
func (c *Client) Mkdir(rel string) error { _, err := c.do(request{Op: opMkdir, Rel: rel}); return err }

// Symlink implements catalog.Sink.
func (c *Client) Symlink(rel, target string) error {
	_, err := c.do(request{Op: opSymlink, Rel: rel, Target: target})
	return err
}

// Link implements catalog.Sink.
func (c *Client) Link(rel, target string) error {
	_, err := c.do(request{Op: opLink, Rel: rel, Target: target})
	return err
}

// Mknod implements catalog.Sink.
func (c *Client) Mknod(rel string, mode uint32, major, minor uint32) error {
	_, err := c.do(request{Op: opMknod, Rel: rel, Mode: mode, Major: major, Minor: minor})
	return err
}

// Apply implements catalog.Sink.
func (c *Client) Apply(rel string, e *catalog.Entry, link bool, o catalog.MetaOptions) ([]string, error) {
	eb, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	b, err := c.do(request{Op: opApply, Rel: rel, Entry: eb, Link: link, NoChown: o.NoChown, Lenient: o.Lenient})
	if err != nil {
		return nil, err
	}
	var r applyReply
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return r.Skipped, nil
}

// WriteFile implements catalog.Sink: the request, the content in D frames, an E frame, then the verdict.
func (c *Client) WriteFile(rel string, size uint64, sum *[32]byte, r io.Reader) error {
	cl := &call{res: make(chan result, 1)}
	id, err := c.register(cl)
	if err != nil {
		return err
	}
	defer c.forget(id)
	body, _ := json.Marshal(request{Op: opWrite, Rel: rel, Size: size, Sum: sum})
	if err := c.send(frame{kind: kindQuery, id: id, body: body}); err != nil {
		return err
	}
	buf := make([]byte, ChunkSize)
	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			if err := c.send(frame{kind: kindData, id: id, body: append([]byte(nil), buf[:n]...)}); err != nil {
				return err
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			_ = c.send(frame{kind: kindAbort, id: id})
			<-cl.res
			return rerr
		}
	}
	if err := c.send(frame{kind: kindEnd, id: id}); err != nil {
		return err
	}
	res := <-cl.res
	return res.err
}
