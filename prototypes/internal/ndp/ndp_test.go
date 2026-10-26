package ndp

import (
	"encoding/binary"
	"net/netip"
	"strings"
	"testing"
	"time"
)

var (
	hostMAC = MAC{0x02, 0, 0, 0, 0, 0x01}
	peerMAC = MAC{0x02, 0, 0, 0, 0, 0x02}
	jail    = netip.MustParseAddr("2001:db8:1::5000")
	peer    = netip.MustParseAddr("2001:db8:1::2")
)

func TestSolicitedNode(t *testing.T) {
	a := netip.MustParseAddr("2001:db8:1::abcd:1234")
	if got := SolicitedNode(a).String(); got != "ff02::1:ffcd:1234" {
		t.Fatal(got)
	}
	if SolicitedNodeMAC(a).String() != "33:33:ff:cd:12:34" {
		t.Fatal(SolicitedNodeMAC(a))
	}
}

func TestBuildParseNS(t *testing.T) {
	b := BuildNS(NSParams{EthSrc: peerMAC, EthDst: SolicitedNodeMAC(jail), Src: peer, Dst: SolicitedNode(jail), Target: jail, SLLA: peerMAC})
	f, err := ParseFrame(b)
	if err != nil {
		t.Fatal(err)
	}
	if !f.IsNS() || !f.MulticastNS() || f.UnicastNS() || f.Target != jail || !f.HasSLLA || f.SLLA != peerMAC || !f.ChecksumOK || f.HopLimit != 255 {
		t.Fatalf("%+v", f)
	}
	b[len(b)-1] ^= 1
	f, _ = ParseFrame(b)
	if f.ChecksumOK {
		t.Fatal("corrupt frame passes checksum")
	}
	u := BuildNS(NSParams{EthSrc: peerMAC, EthDst: hostMAC, Src: peer, Dst: jail, Target: jail, SLLA: peerMAC})
	f, _ = ParseFrame(u)
	if !f.UnicastNS() || f.MulticastNS() {
		t.Fatal("unicast classification")
	}
}

func TestAnswer(t *testing.T) {
	mine := map[netip.Addr]bool{jail: true}
	ns := BuildNS(NSParams{EthSrc: peerMAC, EthDst: SolicitedNodeMAC(jail), Src: peer, Dst: SolicitedNode(jail), Target: jail, SLLA: peerMAC})
	f, _ := ParseFrame(ns)
	na, ok := AnswerFor(f, mine, hostMAC)
	if !ok {
		t.Fatal("no answer")
	}
	g, err := ParseFrame(na)
	if err != nil || !g.IsNA() || !g.ChecksumOK || !g.Solicited || !g.Override || g.Target != jail || g.TLLA != hostMAC || g.EthSrc != hostMAC || g.EthDst != peerMAC || g.Src != jail || g.Dst != peer {
		t.Fatalf("%v %+v", err, g)
	}
	// not ours
	f2, _ := ParseFrame(BuildNS(NSParams{EthSrc: peerMAC, EthDst: SolicitedNodeMAC(peer), Src: peer, Dst: SolicitedNode(peer), Target: netip.MustParseAddr("2001:db8:1::9"), SLLA: peerMAC}))
	if _, ok := AnswerFor(f2, mine, hostMAC); ok {
		t.Fatal("answered foreign target")
	}
	// DAD never answered
	dad, _ := ParseFrame(BuildNS(NSParams{EthSrc: peerMAC, EthDst: SolicitedNodeMAC(jail), Src: netip.IPv6Unspecified(), Dst: SolicitedNode(jail), Target: jail, NoSLLA: true}))
	if !dad.DAD() {
		t.Fatal("dad")
	}
	if _, ok := AnswerFor(dad, mine, hostMAC); ok {
		t.Fatal("answered DAD")
	}
	// hop limit not 255
	raw := BuildNS(NSParams{EthSrc: peerMAC, EthDst: hostMAC, Src: peer, Dst: jail, Target: jail, SLLA: peerMAC})
	raw[14+7] = 64
	f3, _ := ParseFrame(raw)
	if _, ok := AnswerFor(f3, mine, hostMAC); ok {
		t.Fatal("answered off-link hop limit")
	}
}

func TestParseErrors(t *testing.T) {
	if _, err := ParseFrame([]byte{1, 2}); err != ErrShort {
		t.Fatal(err)
	}
	b := BuildNS(NSParams{EthSrc: peerMAC, EthDst: hostMAC, Src: peer, Dst: jail, Target: jail, NoSLLA: true})
	b[12], b[13] = 0x08, 0
	if _, err := ParseFrame(b); err != ErrNotIPv6 {
		t.Fatal(err)
	}
}

func TestFilter(t *testing.T) {
	prog := FilterICMPv6([]uint8{TypeNS, TypeNA}, 65535)
	ns := BuildNS(NSParams{EthSrc: peerMAC, EthDst: hostMAC, Src: peer, Dst: jail, Target: jail, NoSLLA: true})
	na := BuildNA(NAParams{EthSrc: hostMAC, EthDst: peerMAC, Src: jail, Dst: peer, Target: jail, TLLA: hostMAC})
	if RunFilter(prog, ns) != 65535 || RunFilter(prog, na) != 65535 {
		t.Fatal("must accept NS and NA")
	}
	echo := append([]byte(nil), ns...)
	echo[14+40] = TypeEchoRequest
	if RunFilter(prog, echo) != 0 {
		t.Fatal("must drop echo")
	}
	if RunFilter(FilterICMPv6([]uint8{TypeEchoRequest, TypeEchoReply}, 100), echo) != 100 {
		t.Fatal("echo filter")
	}
	tcp := append([]byte(nil), ns...)
	tcp[14+6] = 6
	if RunFilter(prog, tcp) != 0 {
		t.Fatal("must drop non-icmpv6")
	}
	v4 := append([]byte(nil), ns...)
	v4[12], v4[13] = 8, 0
	if RunFilter(prog, v4) != 0 {
		t.Fatal("must drop ipv4")
	}
	if RunFilter(prog, ns[:30]) != 0 {
		t.Fatal("short")
	}
}

func bpfRec(pkt []byte, hdrlen int) []byte {
	h := make([]byte, hdrlen)
	binary.LittleEndian.PutUint32(h[TimevalLen:], uint32(len(pkt)))
	binary.LittleEndian.PutUint32(h[TimevalLen+4:], uint32(len(pkt)))
	binary.LittleEndian.PutUint16(h[TimevalLen+8:], uint16(hdrlen))
	r := append(h, pkt...)
	for len(r)%BPFAlign != 0 {
		r = append(r, 0)
	}
	return r
}

func TestSplitBPF(t *testing.T) {
	p1 := []byte{1, 2, 3, 4, 5}
	p2 := []byte{9, 8, 7, 6, 5, 4, 3, 2, 1}
	buf := append(bpfRec(p1, 32), bpfRec(p2, 32)...)
	got, err := SplitBPF(buf, TimevalLen)
	if err != nil || len(got) != 2 || string(got[0]) != string(p1) || string(got[1]) != string(p2) {
		t.Fatalf("%v %v", err, got)
	}
	if _, err := SplitBPF(buf[:20], TimevalLen); err == nil {
		t.Fatal("truncated must fail")
	}
	trunc := append([]byte(nil), buf[:36]...)
	if _, err := SplitBPF(trunc, TimevalLen); err == nil {
		t.Fatal("caplen beyond buffer must fail")
	}
}

func TestWatchdog(t *testing.T) {
	t0 := time.Unix(1000, 0)
	w := &WD{Deadline: t0.Add(100 * time.Second), FailAfter: 5 * time.Second}
	for i := 0; i < 4; i++ {
		if r, _ := w.Observe(t0.Add(time.Duration(i)*time.Second), false); r {
			t.Fatalf("early restore at %d", i)
		}
	}
	if r, _ := w.Observe(t0.Add(2*time.Second), true); r {
		t.Fatal("ok must not restore")
	}
	if w.MaxRun != 4 || w.Fails != 4 || w.Oks != 1 {
		t.Fatalf("%+v", w)
	}
	for i := 10; i < 15; i++ {
		r, why := w.Observe(t0.Add(time.Duration(i)*time.Second), false)
		if i == 15 && !r {
			t.Fatal(why)
		}
	}
	if r, why := w.Observe(t0.Add(15*time.Second), false); !r || !strings.Contains(why, "probe failing") {
		t.Fatal(r, why)
	}
	w2 := &WD{Deadline: t0.Add(10 * time.Second), FailAfter: 5 * time.Second}
	if r, why := w2.Observe(t0.Add(10*time.Second), true); !r || !strings.Contains(why, "deadline") {
		t.Fatal(r, why)
	}
}

func TestValidateUndo(t *testing.T) {
	ok := [][]string{
		{"ifconfig", "jopbr0", "destroy"},
		{"ifconfig", "jope0a", "destroy"},
		{"ifconfig", "jopbr0", "deletem", "igb0"},
		{"route", "delete", "-inet6", "2001:db8:1::5000/128"},
		{"ndp", "-d", "2001:db8:1::5000"},
		{"jail", "-r", "jop5"},
		{"sysctl", "net.inet6.ip6.forwarding=0"},
	}
	for _, a := range ok {
		if err := ValidateUndo(a); err != nil {
			t.Errorf("%v: %v", a, err)
		}
	}
	bad := [][]string{
		{"ifconfig", "igb0", "destroy"},
		{"ifconfig", "bridge0", "destroy"},
		{"ifconfig", "jopbr0", "deletem", "igb0; rm"},
		{"ifconfig", "igb0", "deletem", "jope0a"},
		{"route", "delete", "-inet6", "default"},
		{"route", "delete", "-inet6", "2001:db8::/64"},
		{"ndp", "-d", "ff02::1"},
		{"ndp", "-a", "-d"},
		{"jail", "-r", "web1"},
		{"sysctl", "net.inet6.ip6.forwarding=2"},
		{"rm", "-rf", "/"},
		{},
	}
	for _, a := range bad {
		if err := ValidateUndo(a); err == nil {
			t.Errorf("%v accepted", a)
		}
	}
	var ran []string
	errs := RunUndo([][]string{{"jail", "-r", "jop5"}, {"rm", "x"}, {"ifconfig", "jopbr0", "destroy"}}, func(a []string) error {
		ran = append(ran, strings.Join(a, " "))
		return nil
	})
	if len(errs) != 1 || strings.Join(ran, ";") != "ifconfig jopbr0 destroy;jail -r jop5" {
		t.Fatal(errs, ran)
	}
}

func TestStateRoundTrip(t *testing.T) {
	p := t.TempDir() + "/s.json"
	s := &State{Pid: 5, ProbeTarget: "fe80::1%em0", Undo: [][]string{{"jail", "-r", "jop5"}}}
	if err := s.Save(p); err != nil {
		t.Fatal(err)
	}
	g, err := LoadState(p)
	if err != nil || g.Pid != 5 || len(g.Undo) != 1 {
		t.Fatal(err, g)
	}
}

const ifc = `igb0: flags=1008943<UP,BROADCAST,RUNNING,PROMISC,SIMPLEX,MULTICAST,LOWER_UP> metric 0 mtu 1500
	options=4e524bb<RXCSUM,TXCSUM>
	ether 0c:c4:7a:aa:bb:cc
	inet6 fe80::ec4:7aff:feaa:bbcc%igb0 prefixlen 64 scopeid 0x1
	inet6 2001:db8:1::10 prefixlen 64
	inet6 2001:db8:1::11 prefixlen 64 tentative
	member: jope0a flags=143<LEARNING,DISCOVER,AUTOEDGE,AUTOPTP>
`

func TestParseIfconfig(t *testing.T) {
	i := ParseIfconfig(ifc)
	if i.Name != "igb0" || !i.HasFlag("PROMISC") || i.Ether.String() != "0c:c4:7a:aa:bb:cc" || len(i.Inet6) != 3 || len(i.Global()) != 2 {
		t.Fatalf("%+v", i)
	}
	if i.Inet6[2].Flags[0] != "tentative" || i.Inet6[1].PrefixLen != 64 || len(i.Members) != 1 {
		t.Fatalf("%+v", i)
	}
	if ll, ok := i.LinkLocal(); !ok || ll.String() != "fe80::ec4:7aff:feaa:bbcc" {
		t.Fatal(ll)
	}
}

func TestParseRouteGet(t *testing.T) {
	r := ParseRouteGet(`   route to: 2001:db8:1::5000
destination: 2001:db8:1::5000
        fib: 0
  interface: jopbr0
      flags: <UP,HOST,DONE,STATIC>
`)
	if r.Interface != "jopbr0" || r.Dest != "2001:db8:1::5000" || !strings.Contains(r.Flags, "HOST") {
		t.Fatalf("%+v", r)
	}
	g := ParseRouteGet("   route to: default\ndestination: ::\n       mask: ::\n    gateway: fe80::1%igb0\n  interface: igb0\n")
	if g.Gateway != "fe80::1%igb0" {
		t.Fatalf("%+v", g)
	}
}

func TestParseNdp(t *testing.T) {
	es := ParseNdpAn(`Neighbor                             Linklayer Address  Netif Expire    S Flags
2001:db8:1::5000                     0c:c4:7a:aa:bb:cc  igb0 permanent  R P
fe80::1%igb0                         00:11:22:33:44:55  igb0 23h59m58s  S R
2001:db8:1::77                       (incomplete)       igb0 expired    N
`)
	if len(es) != 3 || es[0].Iface != "igb0" || es[0].Flags != "P" || es[2].MAC != "(incomplete)" {
		t.Fatalf("%+v", es)
	}
	if len(FindNdp(es, jail)) != 1 {
		t.Fatal("find")
	}
}

func TestTcpdump(t *testing.T) {
	lines := []string{
		"12:00:00.1 02:00:00:00:00:02 > 33:33:ff:00:50:00, ethertype IPv6 (0x86dd), length 86: 2001:db8:1::2 > ff02::1:ff00:5000: ICMP6, neighbor solicitation, who has 2001:db8:1::5000, length 32",
		"12:00:00.2 02:00:00:00:00:01 > 02:00:00:00:00:02, ethertype IPv6 (0x86dd), length 86: 2001:db8:1::5000 > 2001:db8:1::2: ICMP6, neighbor advertisement, tgt is 2001:db8:1::5000, length 32",
		"12:00:05.0 02:00:00:00:00:02 > 02:00:00:00:00:01, ethertype IPv6 (0x86dd), length 86: 2001:db8:1::2 > 2001:db8:1::5000: ICMP6, neighbor solicitation, who has 2001:db8:1::5000, length 32",
		"12:00:06.0 02:00:00:00:00:01 > 33:33:ff:00:50:00, ethertype IPv6 (0x86dd), length 86: 2001:db8:1::10 > ff02::1:ff00:5000: ICMP6, neighbor solicitation, who has 2001:db8:1::5000, length 32",
		"garbage",
	}
	m, u, na, h := SummarizeTcpdump(lines, jail, "02:00:00:00:00:01")
	if m != 2 || u != 1 || na != 1 || h != 1 {
		t.Fatal(m, u, na, h)
	}
}

func TestHypotheses(t *testing.T) {
	mk := func(p, j bool, m, u int) ComboResult {
		return ComboResult{Promisc: p, Joined: j, PromiscFlag: p, McastRan: true, UcastRan: true, Mcast: Tally{McastNS: m}, Ucast: Tally{UcastNS: u}}
	}
	rs := []ComboResult{mk(false, false, 0, 3), mk(false, true, 4, 3), mk(true, false, 4, 3), mk(true, true, 4, 3)}
	if o, _ := EvalUnicast(rs); o != Confirmed {
		t.Fatal(o)
	}
	if o, d := EvalMulticast(rs); o != Confirmed || !strings.Contains(d, "joining is what makes") {
		t.Fatal(o, d)
	}
	rs = []ComboResult{mk(false, false, 0, 0), mk(false, true, 0, 0), mk(true, false, 4, 3), mk(true, true, 4, 3)}
	if o, _ := EvalUnicast(rs); o != Refuted {
		t.Fatal(o)
	}
	if o, _ := EvalMulticast(rs); o != Refuted {
		t.Fatal(o)
	}
	rs = []ComboResult{mk(false, false, 0, 0), mk(false, true, 0, 0), mk(true, false, 0, 0), mk(true, true, 0, 0)}
	if o, _ := EvalUnicast(rs); o != Inconclusive {
		t.Fatal(o)
	}
	// "no promisc" combo that had PROMISC set by someone else is invalid
	bad := mk(false, true, 9, 9)
	bad.PromiscFlag = true
	if o, _ := EvalMulticast([]ComboResult{bad, mk(true, true, 0, 0)}); o != Inconclusive {
		t.Fatal(o)
	}
}

func TestTally(t *testing.T) {
	var tl Tally
	for _, b := range [][]byte{
		BuildNS(NSParams{EthSrc: peerMAC, EthDst: SolicitedNodeMAC(jail), Src: peer, Dst: SolicitedNode(jail), Target: jail, SLLA: peerMAC}),
		BuildNS(NSParams{EthSrc: peerMAC, EthDst: hostMAC, Src: peer, Dst: jail, Target: jail, SLLA: peerMAC}),
		BuildNS(NSParams{EthSrc: hostMAC, EthDst: SolicitedNodeMAC(jail), Src: peer, Dst: SolicitedNode(jail), Target: jail, SLLA: hostMAC}),
		BuildNA(NAParams{EthSrc: hostMAC, EthDst: peerMAC, Src: jail, Dst: peer, Target: jail, TLLA: hostMAC}),
	} {
		f, _ := ParseFrame(b)
		tl.Add(f, jail, hostMAC)
	}
	if tl.McastNS != 1 || tl.UcastNS != 1 || tl.HostNS != 1 || tl.NA != 1 || tl.OutNA != 1 {
		t.Fatalf("%+v", tl)
	}
}
