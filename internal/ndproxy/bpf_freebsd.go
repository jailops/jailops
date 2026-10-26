//go:build freebsd

package ndproxy

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"jailops.net/jailops/sdk/api"
)

// This file is the in-process capture of a daemon started with --dev. A production daemon gets its capture from the
// root helper (internal/priv/ndp_freebsd.go), which is what ran on FreeBSD 15.1 (see doc.go). The details marked
// UNVERIFIED below were not run through this path.

const (
	bpfBufLen   = 65536
	readTimeout = 500 * time.Millisecond
	dltEN10MB   = 1
	// UNVERIFIED: BPF_D_IN is 0 on FreeBSD (see sys/net/bpf.h).
	bpfDirIn = 0
)

// BPF is the BPF capture and injector for one uplink. One descriptor serves
// both: frames are written to the same device that reads them.
type BPF struct {
	iface string
	mu    sync.Mutex
	st    *bpfStream
}

// NewBPF returns a Capture and Injector for the uplink.
func NewBPF(iface string) *BPF { return &BPF{iface: iface} }

// Link implements Capture.
func (b *BPF) Link() (LinkInfo, error) { return SystemLink(b.iface) }

func ioctl(fd int, req uintptr, arg unsafe.Pointer) error {
	_, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), req, uintptr(arg))
	if e != 0 {
		return e
	}
	return nil
}

func ioctlU32(fd int, req uintptr, v uint32) error {
	return ioctl(fd, req, unsafe.Pointer(&v))
}

// Open implements Capture.
//
// UNVERIFIED: /dev/bpf is a cloning device on FreeBSD 15; the order of
// ioctls (BIOCSBLEN before BIOCSETIF), BIOCSDIRECTION to skip our own
// transmissions, BIOCSHDRCMPLT to keep our source MAC, and the read timeout
// that lets Close interrupt a blocked read are all assumptions.
func (b *BPF) Open(info LinkInfo, promisc bool) (Stream, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.st != nil {
		_ = b.st.Close()
		b.st = nil
	}
	fd, err := unix.Open("/dev/bpf", unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open /dev/bpf: %w", err)
	}
	fail := func(what string, err error) (Stream, error) {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("bpf %s: %w", what, err)
	}
	blen := uint32(bpfBufLen)
	if err := ioctl(fd, unix.BIOCSBLEN, unsafe.Pointer(&blen)); err != nil {
		return fail("BIOCSBLEN", err)
	}
	var ifr [32]byte // struct ifreq: name[16] + union[16]
	if len(b.iface) >= 16 {
		return fail("BIOCSETIF", errors.New("interface name too long"))
	}
	copy(ifr[:], b.iface)
	if err := ioctl(fd, unix.BIOCSETIF, unsafe.Pointer(&ifr[0])); err != nil {
		return fail("BIOCSETIF", err)
	}
	var dlt uint32
	if err := ioctl(fd, unix.BIOCGDLT, unsafe.Pointer(&dlt)); err != nil {
		return fail("BIOCGDLT", err)
	}
	if dlt != dltEN10MB {
		return fail("BIOCGDLT", fmt.Errorf("link type %d is not Ethernet", dlt))
	}
	if err := ioctl(fd, unix.BIOCGBLEN, unsafe.Pointer(&blen)); err != nil {
		return fail("BIOCGBLEN", err)
	}
	if err := ioctlU32(fd, unix.BIOCIMMEDIATE, 1); err != nil {
		return fail("BIOCIMMEDIATE", err)
	}
	if err := ioctlU32(fd, unix.BIOCSHDRCMPLT, 1); err != nil {
		return fail("BIOCSHDRCMPLT", err)
	}
	if err := ioctlU32(fd, unix.BIOCSDIRECTION, bpfDirIn); err != nil {
		return fail("BIOCSDIRECTION", err)
	}
	tv := unix.Timeval{Sec: int64(readTimeout / time.Second), Usec: int64(readTimeout % time.Second / time.Microsecond)}
	if err := ioctl(fd, unix.BIOCSRTIMEOUT, unsafe.Pointer(&tv)); err != nil {
		return fail("BIOCSRTIMEOUT", err)
	}
	prog := NDFilter()
	insns := make([]unix.BpfInsn, len(prog))
	for i, in := range prog {
		insns[i] = unix.BpfInsn{Code: in.Code, Jt: in.Jt, Jf: in.Jf, K: in.K}
	}
	bp := unix.BpfProgram{Len: uint32(len(insns)), Insns: &insns[0]}
	if err := ioctl(fd, unix.BIOCSETF, unsafe.Pointer(&bp)); err != nil {
		return fail("BIOCSETF", err)
	}
	if promisc {
		if err := ioctl(fd, unix.BIOCPROMISC, nil); err != nil {
			return fail("BIOCPROMISC", err)
		}
	}
	b.st = &bpfStream{fd: fd, buf: make([]byte, blen)}
	return b.st, nil
}

// Send implements Injector. UNVERIFIED: write(2) on a BPF descriptor with
// BIOCSHDRCMPLT set transmits the frame as given.
func (b *BPF) Send(frame []byte) error {
	b.mu.Lock()
	st := b.st
	b.mu.Unlock()
	if st == nil || st.closed.Load() {
		return api.Errorf(api.CodeBusy, "capture is not open")
	}
	_, err := unix.Write(st.fd, frame)
	return err
}

// NewFileStream returns a Stream over a BPF descriptor that is already attached to the uplink, filtered and locked:
// the root helper opens it and passes it to the daemon, which can only read from it.
func NewFileStream(f *os.File) (Stream, error) {
	fd := int(f.Fd())
	var blen uint32
	if err := ioctl(fd, unix.BIOCGBLEN, unsafe.Pointer(&blen)); err != nil {
		return nil, fmt.Errorf("bpf BIOCGBLEN: %w", err)
	}
	return &bpfStream{fd: fd, file: f, buf: make([]byte, blen)}, nil
}

type bpfStream struct {
	fd     int
	file   *os.File // set when the descriptor came from the helper
	buf    []byte
	queue  [][]byte
	closed atomic.Bool
	active atomic.Bool
}

// Next implements Stream.
func (s *bpfStream) Next() ([]byte, error) {
	s.active.Store(true)
	defer s.active.Store(false)
	for len(s.queue) == 0 {
		if s.closed.Load() {
			return nil, errors.New("bpf stream closed")
		}
		n, err := unix.Read(s.fd, s.buf)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return nil, err
		}
		if n == 0 {
			continue // read timeout
		}
		splitBPFRecords(s.buf[:n], int(unsafe.Sizeof(unix.Timeval{})), unix.BPF_ALIGNMENT, func(f []byte) {
			s.queue = append(s.queue, f)
		})
	}
	f := s.queue[0]
	s.queue = s.queue[1:]
	return f, nil
}

// Close implements Stream. It waits (bounded) for a blocked read to notice.
func (s *bpfStream) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	for i := 0; i < 400 && s.active.Load(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if s.file != nil {
		return s.file.Close()
	}
	return unix.Close(s.fd)
}
