//go:build !freebsd

package ndp

import (
	"errors"
	"time"
)

// Listener is a stub: BPF capture is implemented for FreeBSD only.
type Listener struct {
	Iface   string
	Promisc bool
}

// Options mirrors the FreeBSD type.
type Options struct {
	Iface       string
	Promisc     bool
	Types       []uint8
	ReadTimeout time.Duration
	WriteTypes  []uint8
}

// ErrUnsupported is returned off FreeBSD.
var ErrUnsupported = errors.New("ndp: BPF listener is FreeBSD only")

func OpenListener(Options) (*Listener, error)      { return nil, ErrUnsupported }
func (l *Listener) ReadFrames() ([][]byte, error)  { return nil, ErrUnsupported }
func (l *Listener) Write([]byte) error             { return ErrUnsupported }
func (l *Listener) Stats() (uint32, uint32, error) { return 0, 0, ErrUnsupported }
func (l *Listener) Close() error                   { return nil }
