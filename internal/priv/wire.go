package priv

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"

	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/sdk/api"
)

// ProtocolVersion is the wire version. A peer speaking another version is
// refused in the hello exchange with api.CodeVersionSkew.
const ProtocolVersion = 1

// Wire limits.
const (
	// MaxFrame bounds one frame (JSON, after the 4 byte length prefix).
	MaxFrame = 8 << 20
	// ChunkSize is the payload of one stream data frame.
	ChunkSize = 64 << 10
	// Window is the number of unacknowledged data frames per direction and
	// request, so a slow consumer slows its producer instead of growing memory.
	Window = 8
)

// Frame kinds.
const (
	KindHello  = "hello"  // first frame in each direction
	KindReq    = "req"    // client -> server: start an operation
	KindData   = "data"   // either direction: stream payload
	KindEnd    = "end"    // client -> server: end of the request stream
	KindAck    = "ack"    // either direction: one data frame was consumed
	KindCancel = "cancel" // client -> server: abort the request
	KindRes    = "res"    // server -> client: final reply
	// KindCtl is a control message of a session request: client -> server
	// "resize" and "signal", server -> client "started".
	KindCtl = "ctl"
)

// Frame is the one message type of the protocol. A frame is a 4 byte
// big-endian length followed by that many bytes of JSON.
type Frame struct {
	Kind string `json:"k"`
	V    int    `json:"v,omitempty"`
	ID   uint64 `json:"id,omitempty"`
	Op   string `json:"op,omitempty"`
	// Deadline is the absolute time the operation must finish by, in
	// milliseconds since the epoch; 0 means the server default.
	Deadline int64 `json:"dl,omitempty"`
	// Admin carries the admin assertion of the API user the request acts for.
	Admin  *Admin          `json:"admin,omitempty"`
	Args   json.RawMessage `json:"args,omitempty"`
	Result json.RawMessage `json:"res,omitempty"`
	Err    *WireError      `json:"err,omitempty"`
	Data   []byte          `json:"d,omitempty"`
	// Ch names the channel of a server -> client data frame: "" is stdout,
	// "e" is stderr (session requests).
	Ch string `json:"ch,omitempty"`
	// NFds is the number of file descriptors sent with this frame
	// (SCM_RIGHTS).
	NFds int               `json:"nfds,omitempty"`
	Info map[string]string `json:"info,omitempty"`
}

// WireError is an error on the wire. Exit is set for a command that ran and
// failed.
type WireError struct {
	Code    api.Code          `json:"code"`
	Message string            `json:"msg"`
	Hint    string            `json:"hint,omitempty"`
	Detail  map[string]string `json:"detail,omitempty"`
	Exit    *ExitInfo         `json:"exit,omitempty"`
	// Ctx is "deadline" or "canceled" when the helper stopped the operation
	// because its context ended.
	Ctx string `json:"ctx,omitempty"`
}

// ExitInfo carries a run.ExitError.
type ExitInfo struct {
	Name   string   `json:"name"`
	Args   []string `json:"args,omitempty"`
	Code   int      `json:"code"`
	Stderr string   `json:"stderr,omitempty"`
}

// toWire converts an error. Errors that are not *api.Error become
// CodeInternal with their text.
func toWire(err error) *WireError {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return &WireError{Code: api.CodeInternal, Message: err.Error(), Ctx: "deadline"}
	case errors.Is(err, context.Canceled):
		return &WireError{Code: api.CodeInternal, Message: err.Error(), Ctx: "canceled"}
	}
	var ee *run.ExitError
	if errors.As(err, &ee) {
		return &WireError{Code: api.CodeInternal, Message: err.Error(), Exit: &ExitInfo{Name: ee.Name, Args: ee.Args, Code: ee.Code, Stderr: ee.Stderr}}
	}
	var ae *api.Error
	if errors.As(err, &ae) {
		return &WireError{Code: ae.Code, Message: ae.Message, Hint: ae.Hint, Detail: ae.Detail}
	}
	return &WireError{Code: api.CodeInternal, Message: err.Error()}
}

// fromWire is the inverse of toWire.
func fromWire(w *WireError) error {
	if w == nil {
		return nil
	}
	switch w.Ctx {
	case "deadline":
		return context.DeadlineExceeded
	case "canceled":
		return context.Canceled
	}
	if w.Exit != nil {
		return &run.ExitError{Name: w.Exit.Name, Args: w.Exit.Args, Code: w.Exit.Code, Stderr: w.Exit.Stderr}
	}
	return &api.Error{Code: w.Code, Message: w.Message, Hint: w.Hint, Detail: w.Detail}
}

// ErrFrameTooLarge is returned for a frame beyond MaxFrame. The stream is
// not recoverable after it.
var ErrFrameTooLarge = errors.New("priv: frame too large")

// WriteFrame writes one frame.
func WriteFrame(w io.Writer, f *Frame) error {
	b, err := marshalFrame(f)
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

func marshalFrame(f *Frame) ([]byte, error) {
	body, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}
	if len(body) > MaxFrame {
		return nil, ErrFrameTooLarge
	}
	out := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(out, uint32(len(body)))
	copy(out[4:], body)
	return out, nil
}

// ReadFrame reads one frame. A length of zero or above MaxFrame is an error
// before any allocation.
func ReadFrame(r io.Reader) (*Frame, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 || n > MaxFrame {
		return nil, fmt.Errorf("%w: %d bytes", ErrFrameTooLarge, n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	var f Frame
	dec := json.NewDecoder(bytes.NewReader(buf))
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("priv: bad frame: %w", err)
	}
	if dec.More() {
		return nil, errors.New("priv: trailing data in frame")
	}
	return &f, nil
}

// ---- connection with descriptor passing ----

// fdConn wraps a unix socket connection. Writes are serialised. Reads parse
// SCM_RIGHTS control messages and queue the descriptors in arrival order; a
// frame announcing NFds takes them from the queue. A descriptor arrives with
// the first byte of the frame that carries it, so it is queued before that
// frame can be fully read.
type fdConn struct {
	c  *net.UnixConn
	br *bufio.Reader

	wmu sync.Mutex

	fmu sync.Mutex
	fds []*os.File
}

func newFDConn(c *net.UnixConn) *fdConn {
	fc := &fdConn{c: c}
	fc.br = bufio.NewReaderSize(fdReader{fc}, 64<<10)
	return fc
}

type fdReader struct{ fc *fdConn }

// Read implements io.Reader and collects descriptors.
func (r fdReader) Read(p []byte) (int, error) {
	oob := make([]byte, unix.CmsgSpace(4*4))
	n, oobn, flags, _, err := r.fc.c.ReadMsgUnix(p, oob)
	if n < 0 {
		n = 0
	}
	if oobn > 0 {
		r.fc.collect(oob[:oobn])
	}
	if flags&unix.MSG_CTRUNC != 0 {
		// Descriptors were dropped by the kernel; the connection is unusable.
		return n, errors.New("priv: control message truncated")
	}
	return n, err
}

func (fc *fdConn) collect(oob []byte) {
	msgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return
	}
	for _, m := range msgs {
		fds, err := unix.ParseUnixRights(&m)
		if err != nil {
			continue
		}
		fc.fmu.Lock()
		for _, fd := range fds {
			unix.CloseOnExec(fd)
			fc.fds = append(fc.fds, os.NewFile(uintptr(fd), "passed-fd"))
		}
		fc.fmu.Unlock()
	}
}

func (fc *fdConn) takeFDs(n int) ([]*os.File, error) {
	fc.fmu.Lock()
	defer fc.fmu.Unlock()
	if n > len(fc.fds) {
		return nil, errors.New("priv: frame announces descriptors that did not arrive")
	}
	out := fc.fds[:n:n]
	fc.fds = fc.fds[n:]
	return out, nil
}

func (fc *fdConn) closeFDs() {
	fc.fmu.Lock()
	defer fc.fmu.Unlock()
	for _, f := range fc.fds {
		f.Close()
	}
	fc.fds = nil
}

// read returns the next frame and the descriptors it carries.
func (fc *fdConn) read() (*Frame, []*os.File, error) {
	f, err := ReadFrame(fc.br)
	if err != nil {
		return nil, nil, err
	}
	var files []*os.File
	if f.NFds != 0 {
		if f.NFds < 0 || f.NFds > 4 {
			return nil, nil, errors.New("priv: too many descriptors")
		}
		if files, err = fc.takeFDs(f.NFds); err != nil {
			return nil, nil, err
		}
	}
	return f, files, nil
}

// write sends a frame, with descriptors when files is not empty.
func (fc *fdConn) write(f *Frame, files ...*os.File) error {
	f.NFds = len(files)
	b, err := marshalFrame(f)
	if err != nil {
		return err
	}
	fc.wmu.Lock()
	defer fc.wmu.Unlock()
	if len(files) == 0 {
		_, err = fc.c.Write(b)
		return err
	}
	fds := make([]int, len(files))
	for i, fl := range files {
		fds[i] = int(fl.Fd())
	}
	n, _, err := fc.c.WriteMsgUnix(b, unix.UnixRights(fds...), nil)
	if err != nil {
		return err
	}
	if n < len(b) {
		_, err = fc.c.Write(b[n:])
	}
	return err
}

func (fc *fdConn) close() error {
	err := fc.c.Close()
	fc.closeFDs()
	return err
}

var errClosed = errors.New("priv: connection closed")

func isClosedErr(err error) bool {
	return errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE)
}
