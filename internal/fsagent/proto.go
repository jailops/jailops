package fsagent

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"syscall"
)

// Mode is what an agent does.
type Mode string

const (
	// ModeRead answers stat, names and open.
	ModeRead Mode = "read"
	// ModeWrite applies the operations of catalog.Sink.
	ModeWrite Mode = "write"
)

// MaxFrame is the largest frame body.
const MaxFrame = 1 << 20

// ChunkSize is the size of the D frames a file is sent in.
const ChunkSize = 64 << 10

// DefaultWindow is the credit a reader starts with.
const DefaultWindow = 1 << 20

// Frame kinds.
const (
	kindQuery  = 'Q'
	kindReply  = 'R'
	kindError  = 'X'
	kindData   = 'D'
	kindEnd    = 'E'
	kindAbort  = 'A'
	kindCredit = 'C'
)

type frame struct {
	kind byte
	id   uint32
	body []byte
}

func writeFrame(w io.Writer, f frame) error {
	if len(f.body) > MaxFrame {
		return fmt.Errorf("fsagent: frame of %d bytes", len(f.body))
	}
	// one write per frame: frames of different requests are interleaved on one connection
	buf := make([]byte, 9+len(f.body))
	binary.BigEndian.PutUint32(buf[:4], uint32(5+len(f.body)))
	buf[4] = f.kind
	binary.BigEndian.PutUint32(buf[5:9], f.id)
	copy(buf[9:], f.body)
	_, err := w.Write(buf)
	return err
}

func readFrame(r *bufio.Reader) (frame, error) {
	var hdr [9]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return frame{}, err
	}
	n := binary.BigEndian.Uint32(hdr[:4])
	if n < 5 || n > 5+MaxFrame {
		return frame{}, fmt.Errorf("fsagent: frame length %d", n)
	}
	f := frame{kind: hdr[4], id: binary.BigEndian.Uint32(hdr[5:])}
	if n > 5 {
		f.body = make([]byte, n-5)
		if _, err := io.ReadFull(r, f.body); err != nil {
			return frame{}, err
		}
	}
	return f, nil
}

// wireErr is the body of an X frame.
type wireErr struct {
	Msg    string `json:"msg"`
	Errno  int    `json:"errno,omitempty"`
	Socket bool   `json:"socket,omitempty"`
}

// RemoteError is an error the agent reported. It unwraps to the errno when there was one, so
// errors.Is(err, fs.ErrNotExist) and errors.Is(err, unix.EPERM) work across the connection.
type RemoteError struct {
	Msg   string
	Errno syscall.Errno
}

func (e *RemoteError) Error() string { return e.Msg }

func (e *RemoteError) Unwrap() error {
	if e.Errno != 0 {
		return e.Errno
	}
	return nil
}

func encodeErr(err error, socket bool) []byte {
	w := wireErr{Msg: err.Error(), Socket: socket}
	var en syscall.Errno
	if errors.As(err, &en) {
		w.Errno = int(en)
	}
	b, _ := json.Marshal(w)
	return b
}

func decodeErr(body []byte, socketErr error) error {
	var w wireErr
	if err := json.Unmarshal(body, &w); err != nil {
		return fmt.Errorf("fsagent: unreadable error from the agent: %w", err)
	}
	if w.Socket {
		return socketErr
	}
	return &RemoteError{Msg: w.Msg, Errno: syscall.Errno(w.Errno)}
}

// Requests. Op is one of the names below.
type request struct {
	Op     string `json:"op"`
	Path   string `json:"path,omitempty"`
	Name   string `json:"name,omitempty"`
	Window int    `json:"window,omitempty"`

	// write mode
	Rel     string          `json:"rel,omitempty"`
	Target  string          `json:"target,omitempty"`
	Size    uint64          `json:"size,omitempty"`
	Sum     *[32]byte       `json:"sum,omitempty"`
	Mode    uint32          `json:"mode,omitempty"`
	Major   uint32          `json:"major,omitempty"`
	Minor   uint32          `json:"minor,omitempty"`
	Link    bool            `json:"link,omitempty"`
	NoChown bool            `json:"no_chown,omitempty"`
	Lenient bool            `json:"lenient,omitempty"`
	Entry   json.RawMessage `json:"entry,omitempty"`
}

const (
	opStat    = "stat"
	opNames   = "names"
	opOpen    = "open"
	opBegin   = "begin"
	opMkdir   = "mkdir"
	opWrite   = "write"
	opSymlink = "symlink"
	opLink    = "link"
	opMknod   = "mknod"
	opApply   = "apply"
)

type statReply struct {
	Entry json.RawMessage `json:"entry"`
	Nlink uint64          `json:"nlink"`
	Dev   uint64          `json:"dev"`
}

type namesReply struct {
	Names []string `json:"names"`
}

type applyReply struct {
	Skipped []string `json:"skipped,omitempty"`
}
