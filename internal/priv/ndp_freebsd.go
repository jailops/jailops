//go:build freebsd

package priv

import (
	"fmt"
	"net/netip"
	"os"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"jailops.net/jailops/internal/ndproxy"
	"jailops.net/jailops/sdk/api"
)

const (
	ndpBufLen   = 65536
	ndpReadWait = 500 * time.Millisecond
	dltEN10MB   = 1
	bpfDirIn    = 0 // BPF_D_IN
)

// systemNDP is the BPF implementation of NDPDevice.
type systemNDP struct{}

var ndpSend struct {
	mu sync.Mutex
	fd map[string]int // one write descriptor per uplink, attached with a filter that captures nothing
}

func bpfIoctl(fd int, req uintptr, arg unsafe.Pointer) error {
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), req, uintptr(arg)); e != 0 {
		return e
	}
	return nil
}

func bpfIoctlU32(fd int, req uintptr, v uint32) error { return bpfIoctl(fd, req, unsafe.Pointer(&v)) }

// attach sets the buffer size, binds the descriptor to the interface and checks that it is Ethernet. The order
// matters: the buffer size cannot be changed once the descriptor is attached.
func attach(fd int, iface string) error {
	blen := uint32(ndpBufLen)
	if err := bpfIoctl(fd, unix.BIOCSBLEN, unsafe.Pointer(&blen)); err != nil {
		return fmt.Errorf("BIOCSBLEN: %w", err)
	}
	var ifr [32]byte
	copy(ifr[:unix.IFNAMSIZ-1], iface)
	if err := bpfIoctl(fd, unix.BIOCSETIF, unsafe.Pointer(&ifr[0])); err != nil {
		return fmt.Errorf("BIOCSETIF %s: %w", iface, err)
	}
	var dlt uint32
	if err := bpfIoctl(fd, unix.BIOCGDLT, unsafe.Pointer(&dlt)); err != nil {
		return fmt.Errorf("BIOCGDLT: %w", err)
	}
	if dlt != dltEN10MB {
		return fmt.Errorf("%s is not an Ethernet interface (link type %d)", iface, dlt)
	}
	return nil
}

func setFilter(fd int, prog []ndproxy.BPFInsn) error {
	insns := make([]unix.BpfInsn, len(prog))
	for i, in := range prog {
		insns[i] = unix.BpfInsn{Code: in.Code, Jt: in.Jt, Jf: in.Jf, K: in.K}
	}
	bp := unix.BpfProgram{Len: uint32(len(insns)), Insns: &insns[0]}
	return bpfIoctl(fd, unix.BIOCSETF, unsafe.Pointer(&bp))
}

// Capture implements NDPDevice. The descriptor is opened read-only (so that nothing can be written through it),
// attached, given the neighbor discovery filter and then locked: the holder can read it and nothing else.
func (systemNDP) Capture(iface string, promisc bool) (*os.File, error) {
	f, err := os.OpenFile("/dev/bpf", os.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, api.Errorf(api.CodeInternal, "open /dev/bpf: %v", err)
	}
	fail := func(err error) (*os.File, error) {
		f.Close()
		return nil, api.Errorf(api.CodeInternal, "ndp capture on %s: %v", iface, err)
	}
	fd := int(f.Fd())
	if err := attach(fd, iface); err != nil {
		return fail(err)
	}
	if err := bpfIoctlU32(fd, unix.BIOCIMMEDIATE, 1); err != nil {
		return fail(fmt.Errorf("BIOCIMMEDIATE: %w", err))
	}
	if err := bpfIoctlU32(fd, unix.BIOCSDIRECTION, bpfDirIn); err != nil {
		return fail(fmt.Errorf("BIOCSDIRECTION: %w", err))
	}
	tv := unix.Timeval{Sec: int64(ndpReadWait / time.Second), Usec: int64(ndpReadWait % time.Second / time.Microsecond)}
	if err := bpfIoctl(fd, unix.BIOCSRTIMEOUT, unsafe.Pointer(&tv)); err != nil {
		return fail(fmt.Errorf("BIOCSRTIMEOUT: %w", err))
	}
	if err := setFilter(fd, ndproxy.NDFilter()); err != nil {
		return fail(fmt.Errorf("BIOCSETF: %w", err))
	}
	if promisc {
		if err := bpfIoctl(fd, unix.BIOCPROMISC, nil); err != nil {
			return fail(fmt.Errorf("BIOCPROMISC: %w", err))
		}
	}
	if err := bpfIoctl(fd, unix.BIOCLOCK, nil); err != nil {
		return fail(fmt.Errorf("BIOCLOCK: %w", err))
	}
	return f, nil
}

// Send implements NDPDevice with a write descriptor of the helper's own.
func (systemNDP) Send(iface string, frame []byte) error {
	ndpSend.mu.Lock()
	defer ndpSend.mu.Unlock()
	if ndpSend.fd == nil {
		ndpSend.fd = map[string]int{}
	}
	fd, ok := ndpSend.fd[iface]
	if !ok {
		f, err := unix.Open("/dev/bpf", unix.O_RDWR|unix.O_CLOEXEC, 0)
		if err != nil {
			return api.Errorf(api.CodeInternal, "open /dev/bpf: %v", err)
		}
		fail := func(err error) error {
			_ = unix.Close(f)
			return api.Errorf(api.CodeInternal, "ndp send on %s: %v", iface, err)
		}
		if err := attach(f, iface); err != nil {
			return fail(err)
		}
		if err := bpfIoctlU32(f, unix.BIOCSHDRCMPLT, 1); err != nil { // the frame carries its own source address
			return fail(fmt.Errorf("BIOCSHDRCMPLT: %w", err))
		}
		// Capture nothing: a descriptor that is only written to must not collect packets.
		if err := setFilter(f, []ndproxy.BPFInsn{{Code: 0x06, K: 0}}); err != nil {
			return fail(fmt.Errorf("BIOCSETF: %w", err))
		}
		ndpSend.fd[iface], fd = f, f
	}
	if _, err := unix.Write(fd, frame); err != nil {
		_ = unix.Close(fd) // reopen on the next frame (the interface may have been recreated)
		delete(ndpSend.fd, iface)
		return api.Errorf(api.CodeInternal, "ndp send on %s: %v", iface, err)
	}
	return nil
}

func (systemNDP) HardwareAddr(name string) ([6]byte, error)  { return hardwareAddr(name) }
func (systemNDP) LocalAddrs() ([]netip.Addr, error)          { return localAddrs() }
func (systemNDP) HostRoutes() (map[netip.Addr]string, error) { return hostRoutes() }
