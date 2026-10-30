package wireguard

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"jailops.net/jailops/sdk/api"
)

// TokenPrefix starts every join token; the digit is the format version.
const TokenPrefix = "jwg1."

// maxToken bounds what Decode reads.
const maxToken = 8 << 10

// Token is everything a peer needs to join a server. It holds the peer's private key and the preshared key: it is
// a secret, shown once.
type Token struct {
	Name            string   `json:"name"`
	Endpoint        string   `json:"endpoint"`
	ServerPublicKey string   `json:"server_public_key"`
	PrivateKey      string   `json:"private_key"`
	PresharedKey    string   `json:"preshared_key,omitempty"`
	Address         []string `json:"address"`
	AllowedIPs      []string `json:"allowed_ips"`
	DNS             []string `json:"dns,omitempty"`
	Keepalive       int      `json:"keepalive,omitempty"`
}

// Check validates every field. Errors never repeat a key.
func (t Token) Check() error {
	if err := ValidName("name", t.Name); err != nil {
		return err
	}
	if _, _, err := ParseEndpoint("endpoint", t.Endpoint); err != nil {
		return err
	}
	if err := CheckKey("server_public_key", t.ServerPublicKey); err != nil {
		return err
	}
	if err := CheckKey("private_key", t.PrivateKey); err != nil {
		return err
	}
	if t.PresharedKey != "" {
		if err := CheckKey("preshared_key", t.PresharedKey); err != nil {
			return err
		}
	}
	addrs, err := ParsePrefixes("address", t.Address, true)
	if err != nil {
		return err
	}
	if len(addrs) == 0 {
		return invalid("address", "the token carries no address")
	}
	if _, err := ParsePrefixes("allowed_ips", t.AllowedIPs, false); err != nil {
		return err
	}
	for _, d := range t.DNS {
		if _, err := netip.ParseAddr(d); err != nil {
			return invalid("dns", "dns server %q is not an address", d)
		}
	}
	if t.Keepalive < 0 || t.Keepalive > MaxKeepalive {
		return invalid("keepalive", "keepalive must be 0 to %d seconds", MaxKeepalive)
	}
	return nil
}

// Encode returns "jwg1.<base64url(JSON)>".
func (t Token) Encode() (string, error) {
	if err := t.Check(); err != nil {
		return "", err
	}
	b, err := json.Marshal(t)
	if err != nil {
		return "", err
	}
	return TokenPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// DecodeToken parses and validates a token. A token that is not one says so without echoing it.
func DecodeToken(s string) (Token, error) {
	s = strings.TrimSpace(s)
	bad := func(why string) (Token, error) {
		return Token{}, api.Errorf(api.CodeInvalid, "not a WireGuard join token: %s", why).
			WithHint("copy the whole token that `jailops wireguard peer add` printed; it starts with " + TokenPrefix)
	}
	if !strings.HasPrefix(s, TokenPrefix) {
		return bad("it does not start with " + TokenPrefix)
	}
	if len(s) > maxToken {
		return bad("it is too long")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s[len(TokenPrefix):], "="))
	if err != nil {
		return bad("it is not base64url")
	}
	var t Token
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil {
		return bad("its content is not readable")
	}
	if err := t.Check(); err != nil {
		return Token{}, err
	}
	return t, nil
}

// QuickConfig renders the token as the configuration file the WireGuard apps read: an [Interface]
// with the private key, addresses and DNS, and one [Peer] for the server. Every value was validated by Check, so
// nothing needs quoting.
func (t Token) QuickConfig() (string, error) {
	if err := t.Check(); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# WireGuard configuration of %s, made by JailOps. It holds a private key: keep it secret.\n", t.Name)
	b.WriteString("[Interface]\n")
	b.WriteString("PrivateKey = " + t.PrivateKey + "\n")
	b.WriteString("Address = " + strings.Join(t.Address, ", ") + "\n")
	if len(t.DNS) > 0 {
		b.WriteString("DNS = " + strings.Join(t.DNS, ", ") + "\n")
	}
	b.WriteString("\n[Peer]\n")
	b.WriteString("PublicKey = " + t.ServerPublicKey + "\n")
	if t.PresharedKey != "" {
		b.WriteString("PresharedKey = " + t.PresharedKey + "\n")
	}
	b.WriteString("Endpoint = " + t.Endpoint + "\n")
	b.WriteString("AllowedIPs = " + strings.Join(t.AllowedIPs, ", ") + "\n")
	if t.Keepalive > 0 {
		b.WriteString("PersistentKeepalive = " + strconv.Itoa(t.Keepalive) + "\n")
	}
	return b.String(), nil
}
