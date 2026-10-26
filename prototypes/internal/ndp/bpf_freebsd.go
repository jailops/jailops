//go:build freebsd

package ndp

import (
	"errors"
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Listener is a BPF descriptor bound to one interface. Written without access to FreeBSD:
// every ioctl sequence below is UNVERIFIED until prototype 5 has run on FreeBSD 15.
type Listener struct {
	fd      int
	buf     []byte
	Iface   string
	Promisc bool
	closed  bool
}

// Options for OpenListener.
type Options struct {
	Iface       string
	Promisc     bool // BIOCPROMISC. Not requested means: never requested by this descriptor.
	Types       []uint8
	ReadTimeout time.Duration
	// WriteTypes, when set, installs a write filter (BIOCSETWF) so the descriptor can only
	// transmit ICMPv6 of these types. UNVERIFIED: BIOCSETWF semantics on FreeBSD 15.
	WriteTypes []uint8
}

// Missing in x/sys? All of these were checked present for freebsd/amd64 and freebsd/arm64
// in x/sys v0.49.0 (BIOCSETIF BIOCGBLEN BIOCSBLEN BIOCIMMEDIATE BIOCPROMISC BIOCSETF BIOCSETWF
// BIOCFLUSH BIOCGDLT BIOCSRTIMEOUT BIOCSSEESENT BIOCSHDRCMPLT BIOCGSTATS, DLT_EN10MB). No constant
// had to be defined locally.

func ioctl(fd int, req uint, arg unsafe.Pointer) error {
	_, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(req), uintptr(arg))
	if e != 0 {
		return e
	}
	return nil
}

func toProg(p []Insn) []unix.BpfInsn {
	out := make([]unix.BpfInsn, len(p))
	for i, in := range p {
		out[i] = unix.BpfInsn{Code: in.Code, Jt: in.Jt, Jf: in.Jf, K: in.K}
	}
	return out
}

func setProg(fd int, req uint, p []Insn) error {
	insns := toProg(p)
	prog := unix.BpfProgram{Len: uint32(len(insns)), Insns: &insns[0]}
	err := ioctl(fd, req, unsafe.Pointer(&prog))
	_ = insns[0]
	return err
}

// OpenListener opens /dev/bpf, binds it to the interface and installs the filter.
func OpenListener(o Options) (*Listener, error) {
	var fd int
	var err error
	// UNVERIFIED: /dev/bpf is a cloning device on FreeBSD 15; fall back to /dev/bpfN.
	for _, p := range []string{"/dev/bpf", "/dev/bpf0", "/dev/bpf1", "/dev/bpf2", "/dev/bpf3"} {
		fd, err = unix.Open(p, unix.O_RDWR|unix.O_CLOEXEC, 0)
		if err == nil {
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("open bpf: %w", err)
	}
	l := &Listener{fd: fd, Iface: o.Iface, Promisc: o.Promisc}
	fail := func(what string, e error) (*Listener, error) {
		unix.Close(fd)
		return nil, fmt.Errorf("bpf %s on %s: %w", what, o.Iface, e)
	}
	blen := uint32(256 * 1024)
	if err := ioctl(fd, unix.BIOCSBLEN, unsafe.Pointer(&blen)); err != nil {
		return fail("BIOCSBLEN", err)
	}
	var ifr [64]byte
	if len(o.Iface) >= 16 {
		return fail("ifname", errors.New("too long"))
	}
	copy(ifr[:], o.Iface)
	if err := ioctl(fd, unix.BIOCSETIF, unsafe.Pointer(&ifr[0])); err != nil {
		return fail("BIOCSETIF", err)
	}
	var got uint32
	if err := ioctl(fd, unix.BIOCGBLEN, unsafe.Pointer(&got)); err != nil {
		return fail("BIOCGBLEN", err)
	}
	l.buf = make([]byte, got)
	var dlt uint32
	if err := ioctl(fd, unix.BIOCGDLT, unsafe.Pointer(&dlt)); err != nil {
		return fail("BIOCGDLT", err)
	}
	if dlt != unix.DLT_EN10MB {
		return fail("BIOCGDLT", fmt.Errorf("link type %d is not Ethernet", dlt))
	}
	one := uint32(1)
	if err := ioctl(fd, unix.BIOCIMMEDIATE, unsafe.Pointer(&one)); err != nil {
		return fail("BIOCIMMEDIATE", err)
	}
	// See frames the host itself transmits (our own and the kernel's NA). UNVERIFIED default.
	if err := ioctl(fd, unix.BIOCSSEESENT, unsafe.Pointer(&one)); err != nil {
		return fail("BIOCSSEESENT", err)
	}
	// The write path supplies full Ethernet headers.
	if err := ioctl(fd, unix.BIOCSHDRCMPLT, unsafe.Pointer(&one)); err != nil {
		return fail("BIOCSHDRCMPLT", err)
	}
	rt := o.ReadTimeout
	if rt == 0 {
		rt = 250 * time.Millisecond
	}
	tv := unix.NsecToTimeval(rt.Nanoseconds())
	if err := ioctl(fd, unix.BIOCSRTIMEOUT, unsafe.Pointer(&tv)); err != nil {
		return fail("BIOCSRTIMEOUT", err)
	}
	if o.Promisc {
		if err := ioctl(fd, unix.BIOCPROMISC, nil); err != nil {
			return fail("BIOCPROMISC", err)
		}
	}
	if len(o.Types) > 0 {
		if err := setProg(fd, unix.BIOCSETF, FilterICMPv6(o.Types, 262144)); err != nil {
			return fail("BIOCSETF", err)
		}
	}
	if len(o.WriteTypes) > 0 {
		if err := setProg(fd, unix.BIOCSETWF, FilterICMPv6(o.WriteTypes, 262144)); err != nil {
			return fail("BIOCSETWF", err)
		}
	}
	if err := ioctl(fd, unix.BIOCFLUSH, nil); err != nil {
		return fail("BIOCFLUSH", err)
	}
	return l, nil
}

// ReadFrames returns the frames of one read. A timeout returns (nil, nil). The returned
// slices are copies.
func (l *Listener) ReadFrames() ([][]byte, error) {
	if l.closed {
		return nil, errors.New("closed")
	}
	n, err := unix.Read(l.fd, l.buf)
	if err != nil {
		if err == unix.EAGAIN || err == unix.EINTR {
			return nil, nil
		}
		return nil, err
	}
	if n == 0 {
		return nil, nil // UNVERIFIED: a BIOCSRTIMEOUT expiry may return 0 bytes
	}
	recs, err := SplitBPF(l.buf[:n], TimevalLen)
	out := make([][]byte, len(recs))
	for i, r := range recs {
		out[i] = append([]byte(nil), r...)
	}
	return out, err
}

// Write transmits one complete Ethernet frame.
func (l *Listener) Write(frame []byte) error {
	_, err := unix.Write(l.fd, frame)
	return err
}

// Stats returns the kernel's received and dropped counters.
func (l *Listener) Stats() (recv, drop uint32, err error) {
	var s unix.BpfStat
	err = ioctl(l.fd, unix.BIOCGSTATS, unsafe.Pointer(&s))
	return s.Recv, s.Drop, err
}

// Close releases the descriptor (which also ends promiscuous mode requested by it).
func (l *Listener) Close() error {
	if l.closed {
		return nil
	}
	l.closed = true
	return unix.Close(l.fd)
}
