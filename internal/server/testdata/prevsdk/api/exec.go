package api

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
)

// Exec runs a command in a running jail over one HTTP connection that is
// upgraded to a framed, bidirectional stream (design 8.3):
//
//	POST /api/v1/jails/<n>/exec
//	Connection: Upgrade
//	Upgrade: jailops-exec/1
//	<ExecRequest as JSON>
//
// The server answers 101 Switching Protocols and both sides then speak
// frames. Any error before the upgrade is an ordinary JSON error envelope.
// A frame is one type byte, a four byte big-endian payload length and the
// payload; a payload is at most MaxFrame bytes.

// ExecProtocol is the value of the Upgrade header.
const ExecProtocol = "jailops-exec/1"

// MaxFrame is the largest payload of one frame.
const MaxFrame = 64 << 10

// FrameType is the first byte of a frame.
type FrameType byte

// Frame types. Stdin, Resize and Signal flow from the client; Stdout, Stderr
// and Exit from the server. A Stdin frame with an empty payload is end of
// input. Exit carries the exit status as a big-endian int32 and ends the
// session; a TTY session has no Stderr frames.
const (
	FrameStdin  FrameType = 1
	FrameStdout FrameType = 2
	FrameStderr FrameType = 3
	FrameResize FrameType = 4 // rows, cols as two big-endian uint16
	FrameSignal FrameType = 5 // a big-endian int32 signal number
	FrameExit   FrameType = 6
)

// ExecRequest is the body of POST /api/v1/jails/<n>/exec.
type ExecRequest struct {
	// Argv is the command, one element per argument; it is never run by a shell.
	Argv []string `json:"argv,omitempty"`
	// Shell runs the shell of the jail (its "shell" setting, else /bin/sh)
	// instead of Argv. The request fails when that shell does not exist in
	// the jail; it never falls back silently.
	Shell bool `json:"shell,omitempty"`
	// ShellPath overrides the shell of the jail for this request.
	ShellPath string `json:"shell_path,omitempty"`
	TTY       bool   `json:"tty,omitempty"`
	Rows      uint16 `json:"rows,omitempty"`
	Cols      uint16 `json:"cols,omitempty"`
	// Env is a list of NAME=value entries added to the command's environment.
	Env []string `json:"env,omitempty"`
	// User runs the command as this user of the jail.
	User string `json:"user,omitempty"`
}

// WriteFrame writes one frame.
func WriteFrame(w io.Writer, t FrameType, payload []byte) error {
	if len(payload) > MaxFrame {
		return fmt.Errorf("jailops: frame of %d bytes exceeds %d", len(payload), MaxFrame)
	}
	var h [5]byte
	h[0] = byte(t)
	binary.BigEndian.PutUint32(h[1:], uint32(len(payload)))
	if _, err := w.Write(h[:]); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	_, err := w.Write(payload)
	return err
}

// ReadFrame reads one frame. A frame larger than MaxFrame is an error and
// nothing of it is read into memory.
func ReadFrame(r *bufio.Reader) (FrameType, []byte, error) {
	var h [5]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(h[1:])
	if n > MaxFrame {
		return 0, nil, fmt.Errorf("jailops: frame of %d bytes exceeds %d", n, MaxFrame)
	}
	t := FrameType(h[0])
	if t < FrameStdin || t > FrameExit {
		return 0, nil, fmt.Errorf("jailops: unknown frame type %d", h[0])
	}
	if n == 0 {
		return t, nil, nil
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return 0, nil, err
	}
	return t, buf, nil
}

// ResizePayload encodes a window size.
func ResizePayload(rows, cols uint16) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint16(b, rows)
	binary.BigEndian.PutUint16(b[2:], cols)
	return b
}

// ParseResize decodes a window size.
func ParseResize(p []byte) (rows, cols uint16, err error) {
	if len(p) != 4 {
		return 0, 0, fmt.Errorf("jailops: resize frame needs 4 bytes, got %d", len(p))
	}
	return binary.BigEndian.Uint16(p), binary.BigEndian.Uint16(p[2:]), nil
}

// Int32Payload encodes a signal number or an exit status.
func Int32Payload(n int32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(n))
	return b
}

// ParseInt32 decodes a signal number or an exit status.
func ParseInt32(p []byte) (int32, error) {
	if len(p) != 4 {
		return 0, fmt.Errorf("jailops: frame needs 4 bytes, got %d", len(p))
	}
	return int32(binary.BigEndian.Uint32(p)), nil
}
