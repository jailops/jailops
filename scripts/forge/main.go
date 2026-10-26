//go:build freebsd

// forge sends forged Ethernet/ARP/ICMPv6 frames from inside a jail, for the PF test matrix
// (scripts/ci-freebsd-forge.sh). It is a test tool only: built for FreeBSD, run as root from the host with
// `jexec <jail> /tmp/forge ...`, and needs /dev/bpf unhidden in the jail's devfs (the matrix does that).
// Nothing in JailOps itself uses it. DHCP probes in the matrix use the real dhclient(8), not a forged frame here.
//
//	forge arp -if IF -eth-dst MAC -eth-src MAC -op reply|request -sha MAC -spa IP -tha MAC -tpa IP
//	forge nd  -type na|ns|ra|ptb -if IF -src IP -dst IP -target IP [-lladdr MAC]
package main

import (
	"flag"
	"fmt"
	"net"
	"net/netip"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

func die(a ...any) {
	fmt.Println(a...)
	os.Exit(1)
}

func mac(s string) []byte {
	hw, err := net.ParseMAC(s)
	if err != nil {
		die("bad MAC", s)
	}
	return hw
}

func ip4(s string) []byte { a := netip.MustParseAddr(s).As4(); return a[:] }

// bpfWrite writes one complete Ethernet frame on ifn through bpf; the frame carries its own source address.
func bpfWrite(ifn string, frame []byte) {
	fd, err := unix.Open("/dev/bpf", unix.O_RDWR, 0)
	if err != nil {
		die("open /dev/bpf:", err)
	}
	var ifr [32]byte
	copy(ifr[:], ifn)
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(unix.BIOCSETIF), uintptr(unsafe.Pointer(&ifr[0]))); e != 0 {
		die("BIOCSETIF:", e)
	}
	if err := unix.IoctlSetPointerInt(fd, unix.BIOCSHDRCMPLT, 1); err != nil {
		die("BIOCSHDRCMPLT:", err)
	}
	if _, err := unix.Write(fd, frame); err != nil {
		die("write:", err)
	}
}

func arpCmd(args []string) {
	fs := flag.NewFlagSet("arp", flag.ExitOnError)
	ifn := fs.String("if", "", "interface")
	ed := fs.String("eth-dst", "ff:ff:ff:ff:ff:ff", "frame destination")
	es := fs.String("eth-src", "", "frame source")
	op := fs.String("op", "reply", "reply or request")
	sha := fs.String("sha", "", "sender hardware address")
	spa := fs.String("spa", "", "sender protocol address")
	tha := fs.String("tha", "00:00:00:00:00:00", "target hardware address")
	tpa := fs.String("tpa", "", "target protocol address")
	fs.Parse(args)
	f := append(mac(*ed), mac(*es)...)
	f = append(f, 0x08, 0x06, 0, 1, 8, 0, 6, 4, 0, 1)
	if *op == "reply" {
		f[len(f)-1] = 2
	}
	f = append(f, mac(*sha)...)
	f = append(f, ip4(*spa)...)
	f = append(f, mac(*tha)...)
	f = append(f, ip4(*tpa)...)
	bpfWrite(*ifn, f)
	fmt.Println("sent arp", *op, "from", *spa, *sha, "to", *tpa, "frame dst", *ed)
}

func ndCmd(args []string) {
	fs := flag.NewFlagSet("nd", flag.ExitOnError)
	typ := fs.String("type", "na", "na, ns, ra or ptb")
	src := fs.String("src", "", "source address (bound)")
	dst := fs.String("dst", "ff02::1", "destination")
	target := fs.String("target", "", "target address")
	ifn := fs.String("if", "", "interface")
	lla := fs.String("lladdr", "", "link-layer address option")
	fs.Parse(args)
	t := netip.Addr{}
	if *target != "" {
		t = netip.MustParseAddr(*target)
	}
	var body []byte
	var ty byte
	switch *typ {
	case "ra":
		ty = 134
		body = []byte{64, 0, 0x07, 0x08, 0, 0, 0, 0, 0, 0, 0, 0}
		pi := []byte{3, 4, 64, 0xc0, 0, 0x27, 0x8d, 0, 0, 0x09, 0x3a, 0x80, 0, 0, 0, 0}
		pi = append(pi, netip.MustParseAddr("2001:db8:dead::").AsSlice()...)
		body = append(body, pi...)
	case "ptb":
		v := netip.MustParseAddr(*dst)
		o := netip.MustParseAddr(*target)
		hdr := make([]byte, 40)
		hdr[0] = 0x60
		hdr[4], hdr[5] = 0x05, 0x88
		hdr[6], hdr[7] = 58, 64
		copy(hdr[8:], v.AsSlice())
		copy(hdr[24:], o.AsSlice())
		echo := []byte{128, 0, 0, 0, 0x12, 0x34, 0, 1}
		body = append([]byte{0, 0, 5, 0}, hdr...)
		body = append(body, echo...)
		ty = 2
	case "na":
		ty = 136
		body = append([]byte{0x20, 0, 0, 0}, t.AsSlice()...)
	default: // ns
		ty = 135
		body = append([]byte{0, 0, 0, 0}, t.AsSlice()...)
	}
	if *lla != "" {
		hw := mac(*lla)
		o := byte(2)
		if *typ == "ns" {
			o = 1
		}
		body = append(body, o, 1)
		body = append(body, hw...)
	}
	msg := append([]byte{ty, 0, 0, 0}, body...)
	fd, err := unix.Socket(unix.AF_INET6, unix.SOCK_RAW, unix.IPPROTO_ICMPV6)
	if err != nil {
		die("socket:", err)
	}
	must := func(e error) {
		if e != nil {
			die(e)
		}
	}
	must(unix.SetsockoptInt(fd, unix.IPPROTO_IPV6, unix.IPV6_CHECKSUM, 2))
	must(unix.SetsockoptInt(fd, unix.IPPROTO_IPV6, unix.IPV6_UNICAST_HOPS, 255))
	must(unix.SetsockoptInt(fd, unix.IPPROTO_IPV6, unix.IPV6_MULTICAST_HOPS, 255))
	ifi, err := net.InterfaceByName(*ifn)
	must(err)
	must(unix.SetsockoptInt(fd, unix.IPPROTO_IPV6, unix.IPV6_MULTICAST_IF, ifi.Index))
	sa := &unix.SockaddrInet6{}
	if *src != "" {
		a := netip.MustParseAddr(*src)
		sa.Addr = a.As16()
		if a.IsLinkLocalUnicast() {
			sa.ZoneId = uint32(ifi.Index)
		}
		must(unix.Bind(fd, sa))
	}
	d := netip.MustParseAddr(*dst)
	da := &unix.SockaddrInet6{Addr: d.As16()}
	if d.IsLinkLocalUnicast() || d.IsMulticast() {
		da.ZoneId = uint32(ifi.Index)
	}
	must(unix.Sendto(fd, msg, 0, da))
	fmt.Println("sent", *typ, "src", *src, "dst", *dst, "target", *target)
}

func main() {
	if len(os.Args) < 2 {
		die("usage: forge arp|nd ...")
	}
	switch os.Args[1] {
	case "arp":
		arpCmd(os.Args[2:])
	case "nd":
		ndCmd(os.Args[2:])
	default:
		die("unknown subcommand", os.Args[1])
	}
}
