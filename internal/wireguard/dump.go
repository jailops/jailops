package wireguard

import (
	"net/netip"
	"strconv"
	"strings"
)

// Dump is what `wg show <if> dump` reports, without the secrets: the private key of the interface and the
// preshared keys of the peers are read past and never kept.
type Dump struct {
	PublicKey  string
	ListenPort int
	Peers      []DumpPeer
}

// DumpPeer is one peer line.
type DumpPeer struct {
	PublicKey  string
	Endpoint   string
	AllowedIPs []string
	// LatestHandshake is seconds since the epoch, 0 when there was none.
	LatestHandshake int64
	RxBytes         int64
	TxBytes         int64
	// Keepalive is the persistent keepalive in seconds, 0 when off.
	Keepalive int
	// PresharedKey says that one is set.
	PresharedKey bool
}

// ParseDump parses the tab-separated output of `wg show <if> dump`:
//
//	private-key  public-key  listen-port  fwmark
//	public-key  preshared-key  endpoint  allowed-ips  latest-handshake  transfer-rx  transfer-tx  persistent-keepalive
//
// "(none)" marks a missing value and "off" a disabled one. Lines that do not have the expected number of fields are
// skipped. ok is false when the interface line is missing.
func ParseDump(out string) (d Dump, ok bool) {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	for i, line := range lines {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if i == 0 {
			if len(f) != 4 {
				return Dump{}, false
			}
			d.PublicKey = keyOrEmpty(f[1])
			d.ListenPort, _ = strconv.Atoi(f[2])
			ok = true
			continue
		}
		if len(f) != 8 || !ValidKey(f[0]) {
			continue
		}
		p := DumpPeer{PublicKey: f[0], PresharedKey: f[1] != "(none)" && f[1] != ""}
		if f[2] != "(none)" {
			p.Endpoint = f[2]
		}
		if f[3] != "(none)" && f[3] != "" {
			for _, a := range strings.Split(f[3], ",") {
				if pr, err := netip.ParsePrefix(strings.TrimSpace(a)); err == nil {
					p.AllowedIPs = append(p.AllowedIPs, pr.Masked().String())
				}
			}
		}
		p.LatestHandshake, _ = strconv.ParseInt(f[4], 10, 64)
		p.RxBytes, _ = strconv.ParseInt(f[5], 10, 64)
		p.TxBytes, _ = strconv.ParseInt(f[6], 10, 64)
		if f[7] != "off" {
			p.Keepalive, _ = strconv.Atoi(f[7])
		}
		d.Peers = append(d.Peers, p)
	}
	return d, ok
}

func keyOrEmpty(s string) string {
	if ValidKey(s) {
		return s
	}
	return ""
}
