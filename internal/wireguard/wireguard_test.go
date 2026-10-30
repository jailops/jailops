package wireguard

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run go test -update)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs:\n--- want\n%s\n--- got\n%s", p, want, got)
	}
}

func token() Token {
	return Token{Name: "laptop", Endpoint: "vpn.example.com:51820", ServerPublicKey: FakeKey(1), PrivateKey: FakeKey(2), PresharedKey: FakeKey(3),
		Address: []string{"10.99.0.2/32", "fd99::2/128"}, AllowedIPs: []string{"10.99.0.0/24", "fd99::/64", "10.20.0.0/24"}, DNS: []string{"10.20.0.1"}, Keepalive: 25}
}

func TestTokenRoundTrip(t *testing.T) {
	enc, err := token().Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, TokenPrefix) || strings.ContainsAny(enc, "+/= \n") {
		t.Fatalf("not base64url: %s", enc)
	}
	back, err := DecodeToken("  " + enc + "\n")
	if err != nil {
		t.Fatal(err)
	}
	b1, _ := json.Marshal(token())
	b2, _ := json.Marshal(back)
	if string(b1) != string(b2) {
		t.Fatalf("round trip:\n%s\n%s", b1, b2)
	}
	conf, err := back.QuickConfig()
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "quick_config", conf)
}

func TestTokenRefusals(t *testing.T) {
	mk := func(f func(*Token)) string {
		tk := token()
		f(&tk)
		b, _ := json.Marshal(tk)
		return TokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	}
	secret := FakeKey(2)
	for what, s := range map[string]string{
		"empty":          "",
		"wrong prefix":   "jwg2." + strings.TrimPrefix(mk(func(*Token) {}), TokenPrefix),
		"not base64":     TokenPrefix + "!!!",
		"not json":       TokenPrefix + base64.RawURLEncoding.EncodeToString([]byte("hello")),
		"unknown field":  TokenPrefix + base64.RawURLEncoding.EncodeToString([]byte(`{"name":"x","shell":"rm"}`)),
		"too long":       TokenPrefix + strings.Repeat("A", 9000),
		"bad name":       mk(func(t *Token) { t.Name = "Laptop!" }),
		"bad endpoint":   mk(func(t *Token) { t.Endpoint = "vpn.example.com" }),
		"short key":      mk(func(t *Token) { t.PrivateKey = secret[:40] + "==" }),
		"bad psk":        mk(func(t *Token) { t.PresharedKey = "x" + secret[1:] + "x" }),
		"no address":     mk(func(t *Token) { t.Address = nil }),
		"bad address":    mk(func(t *Token) { t.Address = []string{"10.99.0.2/33"} }),
		"bad allowed ip": mk(func(t *Token) { t.AllowedIPs = []string{"all"} }),
		"bad dns":        mk(func(t *Token) { t.DNS = []string{"dns.example.com"} }),
		"bad keepalive":  mk(func(t *Token) { t.Keepalive = -5 }),
	} {
		_, err := DecodeToken(s)
		if !api.IsCode(err, api.CodeInvalid) {
			t.Errorf("%s: %v", what, err)
		}
		if err != nil && strings.Contains(err.Error(), secret[:16]) {
			t.Errorf("%s: the error repeats the key", what)
		}
	}
}

func conf() Conf {
	return Conf{PrivateKey: FakeKey(1), ListenPort: 51820, Peers: []ConfPeer{
		{PublicKey: FakeKey(2), PresharedKey: FakeKey(3), AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.99.0.2/32"), netip.MustParsePrefix("fd99::2/128")}},
		{PublicKey: FakeKey(4), AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.99.0.3/32")}, Endpoint: "198.51.100.7:51820", Keepalive: 25},
	}}
}

func TestConfRender(t *testing.T) {
	s, err := conf().Render()
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "syncconf", s)
	c := conf()
	c.ListenPort = 0
	s, _ = c.Render()
	if strings.Contains(s, "ListenPort") {
		t.Error("a client got a listen port")
	}
	for what, f := range map[string]func(*Conf){
		"bad private key":   func(c *Conf) { c.PrivateKey = "x" },
		"bad peer key":      func(c *Conf) { c.Peers[0].PublicKey = "x" },
		"peer twice":        func(c *Conf) { c.Peers[1].PublicKey = c.Peers[0].PublicKey },
		"bad psk":           func(c *Conf) { c.Peers[0].PresharedKey = "abc" },
		"endpoint newline":  func(c *Conf) { c.Peers[1].Endpoint = "a\n[Peer]:1" },
		"port out of range": func(c *Conf) { c.ListenPort = 65536 },
		"keepalive":         func(c *Conf) { c.Peers[1].Keepalive = MaxKeepalive + 1 },
	} {
		c := conf()
		f(&c)
		if _, err := c.Render(); err == nil {
			t.Errorf("%s accepted", what)
		}
	}
}

const dump = "PRIVATEKEYHIDDENxxxxxxxxxxxxxxxxxxxxxxxxxxx=\t" + "PUB" + "\t51820\toff\n"

func TestParseDump(t *testing.T) {
	priv, pub, p1, p2, psk := FakeKey(1), FakeKey(9), FakeKey(2), FakeKey(4), FakeKey(3)
	out := strings.Join([]string{
		priv + "\t" + pub + "\t51820\toff",
		p1 + "\t" + psk + "\t198.51.100.7:41000\t10.99.0.2/32,fd99::2/128\t1761800000\t1024\t2048\toff",
		p2 + "\t(none)\t(none)\t10.99.0.3/32\t0\t0\t0\t25",
		"garbage line",
		"short\tline",
	}, "\n") + "\n"
	d, ok := ParseDump(out)
	if !ok || d.PublicKey != pub || d.ListenPort != 51820 || len(d.Peers) != 2 {
		t.Fatalf("%+v %v", d, ok)
	}
	a, b := d.Peers[0], d.Peers[1]
	if a.PublicKey != p1 || !a.PresharedKey || a.Endpoint != "198.51.100.7:41000" || strings.Join(a.AllowedIPs, ",") != "10.99.0.2/32,fd99::2/128" ||
		a.LatestHandshake != 1761800000 || a.RxBytes != 1024 || a.TxBytes != 2048 || a.Keepalive != 0 {
		t.Errorf("peer 1 %+v", a)
	}
	if b.PresharedKey || b.Endpoint != "" || b.LatestHandshake != 0 || b.Keepalive != 25 {
		t.Errorf("peer 2 %+v", b)
	}
	j, _ := json.Marshal(d)
	if strings.Contains(string(j), priv) || strings.Contains(string(j), psk) {
		t.Fatal("the parse kept a secret")
	}
	if _, ok := ParseDump(""); ok {
		t.Error("empty output parsed")
	}
	if _, ok := ParseDump(dump[:10]); ok {
		t.Error("a broken interface line parsed")
	}
	// what the kernel holds matches the configuration it was given
	c := Conf{PrivateKey: priv, ListenPort: 51820, Peers: []ConfPeer{
		{PublicKey: p1, PresharedKey: psk, AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.99.0.2/32"), netip.MustParsePrefix("fd99::2/128")}},
		{PublicKey: p2, AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.99.0.3/32")}, Keepalive: 25},
	}}
	if !c.Matches(pub, d) {
		t.Error("an equal configuration differs")
	}
	c.Peers[1].Keepalive = 30
	if c.Matches(pub, d) {
		t.Error("a changed keepalive matches")
	}
	c.Peers[1].Keepalive = 25
	c.Peers[0].PresharedKey = ""
	if c.Matches(pub, d) {
		t.Error("a missing preshared key matches")
	}
	c.Peers[0].PresharedKey = psk
	c.Peers[1].Endpoint = "vpn.example.com:51820"
	if !c.Matches(pub, d) {
		t.Error("a name endpoint is compared")
	}
	c.Peers[1].Endpoint = "203.0.113.1:51820"
	if c.Matches(pub, d) {
		t.Error("another endpoint address matches")
	}
}

func TestPoolAllocator(t *testing.T) {
	p := netip.MustParsePrefix("10.99.0.0/24")
	used := []netip.Addr{netip.MustParseAddr("10.99.0.1"), netip.MustParseAddr("10.99.0.2"), netip.MustParseAddr("10.99.0.4")}
	if a, ok := NextAddress(p, used); !ok || a.String() != "10.99.0.3" {
		t.Errorf("%v %v", a, ok)
	}
	small := netip.MustParsePrefix("10.99.0.0/30") // .1 and .2 are hosts
	if a, ok := NextAddress(small, []netip.Addr{netip.MustParseAddr("10.99.0.1"), netip.MustParseAddr("10.99.0.2")}); ok {
		t.Errorf("a full /30 gave %v (the broadcast address)", a)
	}
	if a, ok := NextAddress(netip.MustParsePrefix("fd99::/64"), []netip.Addr{netip.MustParseAddr("fd99::1")}); !ok || a.String() != "fd99::2" {
		t.Errorf("%v %v", a, ok)
	}
	if a, ok := NextAddress(netip.MustParsePrefix("10.99.0.5/32"), nil); ok {
		t.Errorf("a /32 pool gave %v", a)
	}
	spec := api.WireGuardSpec{Mode: api.WireGuardServer, Address: []string{"10.99.0.1/24", "fd99::1/64"}, AllowedIPs: []string{"10.99.0.0/24", "fd99::/64"},
		Peers: []api.WireGuardPeer{{Name: "a", PublicKey: FakeKey(1), AllowedIPs: []string{"10.99.0.2/32", "fd99::2/128"}}}}
	got, err := PeerAddresses(spec)
	if err != nil || len(got) != 2 || got[0].String() != "10.99.0.3/32" || got[1].String() != "fd99::3/128" {
		t.Fatalf("%v %v", got, err)
	}
	spec.AllowedIPs = []string{"10.99.0.0/30"}
	spec.Address = []string{"10.99.0.1/30"}
	if _, err := PeerAddresses(spec); !api.IsCode(err, api.CodePoolExhausted) {
		t.Errorf("a full pool: %v", err)
	}
}

func TestValidateSpec(t *testing.T) {
	srv := api.WireGuardSpec{Mode: api.WireGuardServer, Interface: "wg-office", Address: []string{"10.99.0.1/24"}, ListenPort: 51820, AllowedIPs: []string{"10.99.0.0/24"}}
	if err := ValidateSpec("office", srv); err != nil {
		t.Fatal(err)
	}
	cl := api.WireGuardSpec{Mode: api.WireGuardClient, Interface: "wg-home", Address: []string{"10.99.0.2/32"}, Endpoint: "[2001:db8::1]:51820",
		PeerPublicKey: FakeKey(1), AllowedIPs: []string{"10.99.0.0/24"}, Keepalive: 25}
	if err := ValidateSpec("home", cl); err != nil {
		t.Fatal(err)
	}
	for what, c := range map[string]struct {
		name string
		s    api.WireGuardSpec
		f    func(*api.WireGuardSpec)
	}{
		"name":         {"Office", srv, func(*api.WireGuardSpec) {}},
		"iface prefix": {"office", srv, func(s *api.WireGuardSpec) { s.Interface = "tun0" }},
		"iface long":   {"office", srv, func(s *api.WireGuardSpec) { s.Interface = "wg-abcdefghijklm" }},
		"no port":      {"office", srv, func(s *api.WireGuardSpec) { s.ListenPort = 0 }},
		"loopback":     {"office", srv, func(s *api.WireGuardSpec) { s.Address = []string{"127.0.0.2/8"} }},
		"peer default route": {"office", srv, func(s *api.WireGuardSpec) {
			s.Peers = []api.WireGuardPeer{{Name: "p", PublicKey: FakeKey(1), AllowedIPs: []string{"0.0.0.0/0"}}}
		}},
		"peers overlap": {"office", srv, func(s *api.WireGuardSpec) {
			s.Peers = []api.WireGuardPeer{{Name: "p", PublicKey: FakeKey(1), AllowedIPs: []string{"10.99.0.2"}}, {Name: "q", PublicKey: FakeKey(2), AllowedIPs: []string{"10.99.0.2/32"}}}
		}},
		"peer same key": {"office", srv, func(s *api.WireGuardSpec) {
			s.Peers = []api.WireGuardPeer{{Name: "p", PublicKey: FakeKey(1), AllowedIPs: []string{"10.99.0.2"}}, {Name: "q", PublicKey: FakeKey(1), AllowedIPs: []string{"10.99.0.3"}}}
		}},
		"peer holds server": {"office", srv, func(s *api.WireGuardSpec) {
			s.Peers = []api.WireGuardPeer{{Name: "p", PublicKey: FakeKey(1), AllowedIPs: []string{"10.99.0.1"}}}
		}},
		"client full tunnel": {"home", cl, func(s *api.WireGuardSpec) { s.AllowedIPs = []string{"0.0.0.0/0"} }},
		"client no endpoint": {"home", cl, func(s *api.WireGuardSpec) { s.Endpoint = "" }},
		"client bad key":     {"home", cl, func(s *api.WireGuardSpec) { s.PeerPublicKey = "x" }},
		"client networks":    {"home", cl, func(s *api.WireGuardSpec) { s.Networks = []string{"lan"} }},
		"client peers":       {"home", cl, func(s *api.WireGuardSpec) { s.Peers = srv.Peers; s.Peers = []api.WireGuardPeer{{}} }},
		"endpoint port":      {"home", cl, func(s *api.WireGuardSpec) { s.Endpoint = "vpn.example.com:99999" }},
		"endpoint host":      {"home", cl, func(s *api.WireGuardSpec) { s.Endpoint = "-bad-.example.com:51820" }},
		"dns":                {"home", cl, func(s *api.WireGuardSpec) { s.DNS = []string{"one.one"} }},
		"keepalive":          {"home", cl, func(s *api.WireGuardSpec) { s.Keepalive = -1 }},
	} {
		s := c.s
		s.Peers = append([]api.WireGuardPeer(nil), s.Peers...)
		c.f(&s)
		if err := ValidateSpec(c.name, s); err == nil {
			t.Errorf("%s accepted", what)
		}
	}
	for _, k := range []string{"", "x", strings.Repeat("A", 44), base64.StdEncoding.EncodeToString(make([]byte, 31)) + "A", base64.StdEncoding.EncodeToString(make([]byte, 33))} {
		if ValidKey(k) {
			t.Errorf("%q is a key", k)
		}
	}
}

func TestDefaults(t *testing.T) {
	s := api.WireGuardSpec{Mode: api.WireGuardServer, Address: []string{"10.99.0.1/24", "fd99::1/64"}}
	Defaults("office", "host.example.com", &s)
	if s.Interface != "wg-office" || s.ListenPort != 51820 || strings.Join(s.AllowedIPs, ",") != "10.99.0.0/24,fd99::/64" || s.Endpoint != "host.example.com:51820" {
		t.Fatalf("%+v", s)
	}
	c := api.WireGuardSpec{Mode: api.WireGuardClient}
	Defaults("home", "host.example.com", &c)
	if c.Keepalive != 25 || c.Endpoint != "" || c.ListenPort != 0 {
		t.Fatalf("%+v", c)
	}
}
