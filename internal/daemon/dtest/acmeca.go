package dtest

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/acme"
)

// ACMECA is a certificate authority that speaks the part of RFC 8555 the daemon uses, written for the tests. It
// covers: the directory with its terms of service, nonces, JWS requests signed with ES256 and checked (jwk for a new
// account, kid after), accounts (refused without agreeing to the terms), an order for several names, one
// authorization per name with an http-01 and a tls-alpn-01 challenge, the validation of either against the address
// the test names (the daemon's real listeners: http-01 fetches the key authorization over plain http, tls-alpn-01
// completes a handshake with acme-tls/1 and checks the acmeIdentifier extension), finalize with the CSR's names
// checked against the order, and the chain as PEM. Not covered: DNS (the CA connects to the address it is given, not
// to what a name resolves to), rate limits, external account binding, revocation, key rollover, order reuse, ports 80
// and 443 and the pf redirect before the daemon.
type ACMECA struct {
	t   testing.TB
	srv *httptest.Server

	mu sync.Mutex
	// HTTPTarget and ALPNTarget are where the CA connects to validate (host:port).
	HTTPTarget, ALPNTarget string
	// Validity is how long issued certificates are valid (default 90 days); Fail refuses every validation.
	Validity time.Duration
	Fail     bool
	// Validations lists "type name ok|error" for each validation.
	Validations []string
	// Issued counts the certificates issued.
	Issued int

	caKey    *ecdsa.PrivateKey
	caCert   *x509.Certificate
	accounts map[string]crypto.PublicKey // account URL -> key
	byThumb  map[string]string
	orders   map[string]*caOrder
	authzs   map[string]*caAuthz
	certs    map[string][]byte
	n        int
	nonces   map[string]bool
}

type caOrder struct {
	names   []string
	authzs  []string
	status  string
	certURL string
	account string
}

type caAuthz struct {
	name    string
	status  string
	account string
	token   string
}

// NewACMECA starts the CA. Its HTTPClient trusts the CA's own https endpoint; Roots holds the root it signs with.
func NewACMECA(t testing.TB) *ACMECA {
	t.Helper()
	ca := &ACMECA{t: t, Validity: 90 * 24 * time.Hour, accounts: map[string]crypto.PublicKey{}, byThumb: map[string]string{},
		orders: map[string]*caOrder{}, authzs: map[string]*caAuthz{}, certs: map[string][]byte{}, nonces: map[string]bool{}}
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test ACME root"}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(10 * 365 * 24 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	ca.caKey, ca.caCert = k, mustParse(t, der)
	ca.srv = httptest.NewTLSServer(http.HandlerFunc(ca.handle))
	t.Cleanup(ca.srv.Close)
	return ca
}

func mustParse(t testing.TB, der []byte) *x509.Certificate {
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// DirectoryURL is the CA's directory.
func (ca *ACMECA) DirectoryURL() string { return ca.srv.URL + "/dir" }

// HTTPClient talks to the CA.
func (ca *ACMECA) HTTPClient() *http.Client { return ca.srv.Client() }

// Roots holds the root of the certificates the CA issues.
func (ca *ACMECA) Roots() *x509.CertPool { p := x509.NewCertPool(); p.AddCert(ca.caCert); return p }

// Terms is the URL of the terms of service the directory names.
func (ca *ACMECA) Terms() string { return ca.srv.URL + "/terms" }

// SetTargets names where the CA validates (both may be set at any time).
func (ca *ACMECA) SetTargets(httpAddr, alpnAddr string) {
	ca.mu.Lock()
	defer ca.mu.Unlock()
	ca.HTTPTarget, ca.ALPNTarget = httpAddr, alpnAddr
}

// Log returns Validations and Issued.
func (ca *ACMECA) Log() ([]string, int) {
	ca.mu.Lock()
	defer ca.mu.Unlock()
	return slices.Clone(ca.Validations), ca.Issued
}

// SetFail makes every validation fail (or succeed again).
func (ca *ACMECA) SetFail(f bool) { ca.mu.Lock(); ca.Fail = f; ca.mu.Unlock() }

func (ca *ACMECA) url(format string, a ...any) string { return ca.srv.URL + fmt.Sprintf(format, a...) }

func (ca *ACMECA) nonce(w http.ResponseWriter) {
	ca.n++
	n := "nonce" + strconv.Itoa(ca.n)
	ca.nonces[n] = true
	w.Header().Set("Replay-Nonce", n)
}

func problem(w http.ResponseWriter, code int, typ, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "urn:ietf:params:acme:error:" + typ, "detail": detail})
}

func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// jws is a flattened JWS request body.
type jws struct {
	Protected string `json:"protected"`
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

type jwsHeader struct {
	Alg   string          `json:"alg"`
	Nonce string          `json:"nonce"`
	URL   string          `json:"url"`
	KID   string          `json:"kid"`
	JWK   json.RawMessage `json:"jwk"`
}

// verify checks a request's signature and returns its header, its payload and the account it was signed by ("" for
// a jwk request).
func (ca *ACMECA) verify(r *http.Request) (jwsHeader, []byte, crypto.PublicKey, error) {
	var h jwsHeader
	var body jws
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		return h, nil, nil, err
	}
	pb, err := base64.RawURLEncoding.DecodeString(body.Protected)
	if err != nil {
		return h, nil, nil, err
	}
	if err := json.Unmarshal(pb, &h); err != nil {
		return h, nil, nil, err
	}
	if !ca.nonces[h.Nonce] {
		return h, nil, nil, fmt.Errorf("bad nonce %q", h.Nonce)
	}
	delete(ca.nonces, h.Nonce)
	if h.URL != ca.srv.URL+r.URL.Path {
		return h, nil, nil, fmt.Errorf("url %q is not %q", h.URL, ca.srv.URL+r.URL.Path)
	}
	var pub crypto.PublicKey
	switch {
	case h.KID != "":
		pub = ca.accounts[h.KID]
		if pub == nil {
			return h, nil, nil, fmt.Errorf("unknown account %q", h.KID)
		}
	case len(h.JWK) > 0:
		var k struct{ Kty, Crv, X, Y string }
		if err := json.Unmarshal(h.JWK, &k); err != nil || k.Kty != "EC" || k.Crv != "P-256" {
			return h, nil, nil, fmt.Errorf("unsupported jwk %s", h.JWK)
		}
		x, _ := base64.RawURLEncoding.DecodeString(k.X)
		y, _ := base64.RawURLEncoding.DecodeString(k.Y)
		pub = &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
	default:
		return h, nil, nil, fmt.Errorf("neither kid nor jwk")
	}
	ek, ok := pub.(*ecdsa.PublicKey)
	if !ok || h.Alg != "ES256" {
		return h, nil, nil, fmt.Errorf("alg %s", h.Alg)
	}
	sig, err := base64.RawURLEncoding.DecodeString(body.Signature)
	if err != nil || len(sig) != 64 {
		return h, nil, nil, fmt.Errorf("signature")
	}
	sum := sha256.Sum256([]byte(body.Protected + "." + body.Payload))
	if !ecdsa.Verify(ek, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		return h, nil, nil, fmt.Errorf("the signature does not verify")
	}
	payload, err := base64.RawURLEncoding.DecodeString(body.Payload)
	if err != nil {
		return h, nil, nil, err
	}
	return h, payload, pub, nil
}

func (ca *ACMECA) handle(w http.ResponseWriter, r *http.Request) {
	ca.mu.Lock()
	defer ca.mu.Unlock()
	ca.nonce(w)
	p := r.URL.Path
	switch {
	case p == "/dir":
		reply(w, 200, map[string]any{"newNonce": ca.url("/nonce"), "newAccount": ca.url("/new-acct"), "newOrder": ca.url("/new-order"),
			"revokeCert": ca.url("/revoke"), "keyChange": ca.url("/key-change"), "meta": map[string]any{"termsOfService": ca.Terms()}})
		return
	case p == "/nonce":
		w.WriteHeader(200)
		return
	}
	if r.Method != http.MethodPost {
		problem(w, 405, "malformed", "POST only")
		return
	}
	h, payload, pub, err := ca.verify(r)
	if err != nil {
		typ := "malformed"
		if strings.HasPrefix(err.Error(), "bad nonce") {
			typ = "badNonce"
		}
		problem(w, 400, typ, err.Error())
		return
	}
	switch {
	case p == "/new-acct":
		var req struct {
			Terms   bool     `json:"termsOfServiceAgreed"`
			Contact []string `json:"contact"`
		}
		_ = json.Unmarshal(payload, &req)
		thumb, _ := acme.JWKThumbprint(pub)
		if u, ok := ca.byThumb[thumb]; ok {
			w.Header().Set("Location", u)
			reply(w, 200, map[string]any{"status": "valid"})
			return
		}
		if !req.Terms {
			problem(w, 403, "userActionRequired", "the terms of service must be agreed to")
			return
		}
		for _, c := range req.Contact {
			if !strings.HasPrefix(c, "mailto:") || !strings.Contains(c, "@") {
				problem(w, 400, "invalidContact", "bad contact "+c)
				return
			}
		}
		u := ca.url("/acct/%d", len(ca.accounts)+1)
		ca.accounts[u], ca.byThumb[thumb] = pub, u
		w.Header().Set("Location", u)
		reply(w, 201, map[string]any{"status": "valid", "contact": req.Contact})
	case p == "/new-order":
		var req struct {
			Identifiers []struct{ Type, Value string } `json:"identifiers"`
		}
		_ = json.Unmarshal(payload, &req)
		o := &caOrder{status: "pending", account: h.KID}
		for _, id := range req.Identifiers {
			if id.Type != "dns" || strings.HasPrefix(id.Value, "*.") {
				problem(w, 400, "rejectedIdentifier", id.Value)
				return
			}
			tok := make([]byte, 16)
			_, _ = rand.Read(tok)
			a := ca.url("/authz/%d", len(ca.authzs)+1)
			ca.authzs[a] = &caAuthz{name: id.Value, status: "pending", account: h.KID, token: base64.RawURLEncoding.EncodeToString(tok)}
			o.names, o.authzs = append(o.names, id.Value), append(o.authzs, a)
		}
		u := ca.url("/order/%d", len(ca.orders)+1)
		ca.orders[u] = o
		w.Header().Set("Location", u)
		reply(w, 201, ca.orderJSON(u, o))
	case strings.HasPrefix(p, "/order/"):
		u := ca.url("%s", p)
		o := ca.orders[u]
		if o == nil {
			problem(w, 404, "malformed", "no order")
			return
		}
		reply(w, 200, ca.orderJSON(u, o))
	case strings.HasPrefix(p, "/authz/"):
		a := ca.authzs[ca.url("%s", p)]
		if a == nil {
			problem(w, 404, "malformed", "no authorization")
			return
		}
		reply(w, 200, ca.authzJSON(ca.url("%s", p), a))
	case strings.HasPrefix(p, "/chal/"):
		// /chal/<authz number>/<type>
		parts := strings.Split(strings.TrimPrefix(p, "/chal/"), "/")
		au := ca.url("/authz/%s", parts[0])
		a := ca.authzs[au]
		if a == nil || len(parts) != 2 {
			problem(w, 404, "malformed", "no challenge")
			return
		}
		keyAuth := a.token + "." + mustThumb(ca.accounts[a.account])
		err := ca.validate(parts[1], a.name, a.token, keyAuth)
		res := "ok"
		if err != nil {
			res = err.Error()
			a.status = "invalid"
		} else {
			a.status = "valid"
		}
		ca.Validations = append(ca.Validations, parts[1]+" "+a.name+" "+res)
		ca.updateOrders()
		reply(w, 200, map[string]any{"type": parts[1], "url": ca.url("%s", p), "token": a.token, "status": a.status})
	case strings.HasPrefix(p, "/finalize/"):
		u := ca.url("/order/%s", strings.TrimPrefix(p, "/finalize/"))
		o := ca.orders[u]
		if o == nil || o.status != "ready" {
			problem(w, 403, "orderNotReady", "the order is not ready")
			return
		}
		var req struct {
			CSR string `json:"csr"`
		}
		_ = json.Unmarshal(payload, &req)
		der, _ := base64.RawURLEncoding.DecodeString(req.CSR)
		csr, err := x509.ParseCertificateRequest(der)
		if err != nil || csr.CheckSignature() != nil {
			problem(w, 400, "badCSR", "the CSR does not parse")
			return
		}
		got, want := slices.Sorted(slices.Values(csr.DNSNames)), slices.Sorted(slices.Values(o.names))
		if !slices.Equal(got, want) {
			problem(w, 400, "badCSR", fmt.Sprintf("the CSR names %v, the order %v", got, want))
			return
		}
		ca.Issued++
		tpl := &x509.Certificate{SerialNumber: big.NewInt(int64(100 + ca.Issued)), Subject: pkix.Name{CommonName: o.names[0]}, DNSNames: o.names,
			NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(ca.Validity), KeyUsage: x509.KeyUsageDigitalSignature,
			ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		cder, err := x509.CreateCertificate(rand.Reader, tpl, ca.caCert, csr.PublicKey, ca.caKey)
		if err != nil {
			problem(w, 500, "serverInternal", err.Error())
			return
		}
		var chain bytes.Buffer
		_ = pem.Encode(&chain, &pem.Block{Type: "CERTIFICATE", Bytes: cder})
		_ = pem.Encode(&chain, &pem.Block{Type: "CERTIFICATE", Bytes: ca.caCert.Raw})
		o.certURL = ca.url("/cert/%d", ca.Issued)
		ca.certs[o.certURL] = chain.Bytes()
		o.status = "valid"
		reply(w, 200, ca.orderJSON(u, o))
	case strings.HasPrefix(p, "/cert/"):
		b := ca.certs[ca.url("%s", p)]
		if b == nil {
			problem(w, 404, "malformed", "no certificate")
			return
		}
		w.Header().Set("Content-Type", "application/pem-certificate-chain")
		_, _ = w.Write(b)
	default:
		problem(w, 404, "malformed", "no such resource")
	}
}

func mustThumb(k crypto.PublicKey) string { s, _ := acme.JWKThumbprint(k); return s }

func (ca *ACMECA) updateOrders() {
	for _, o := range ca.orders {
		if o.status != "pending" {
			continue
		}
		ready := true
		for _, a := range o.authzs {
			switch ca.authzs[a].status {
			case "invalid":
				o.status = "invalid"
			case "valid":
			default:
				ready = false
			}
		}
		if ready && o.status == "pending" {
			o.status = "ready"
		}
	}
}

func (ca *ACMECA) orderJSON(u string, o *caOrder) map[string]any {
	ids := []map[string]string{}
	for _, n := range o.names {
		ids = append(ids, map[string]string{"type": "dns", "value": n})
	}
	m := map[string]any{"status": o.status, "identifiers": ids, "authorizations": o.authzs, "finalize": strings.Replace(u, "/order/", "/finalize/", 1)}
	if o.certURL != "" {
		m["certificate"] = o.certURL
	}
	if o.status == "invalid" {
		m["error"] = map[string]any{"type": "urn:ietf:params:acme:error:unauthorized", "detail": "an authorization failed"}
	}
	return m
}

func (ca *ACMECA) authzJSON(u string, a *caAuthz) map[string]any {
	n := strings.TrimPrefix(u, ca.srv.URL+"/authz/")
	var chals []map[string]any
	for _, typ := range []string{"http-01", "tls-alpn-01"} {
		chals = append(chals, map[string]any{"type": typ, "url": ca.url("/chal/%s/%s", n, typ), "token": a.token, "status": a.status})
	}
	m := map[string]any{"status": a.status, "identifier": map[string]string{"type": "dns", "value": a.name}, "challenges": chals}
	if a.status == "invalid" {
		chals[0]["error"] = map[string]any{"type": "urn:ietf:params:acme:error:connection", "detail": "the challenge failed"}
	}
	return m
}

// validate connects to the daemon the way a CA does (ca.mu held).
func (ca *ACMECA) validate(typ, name, token, keyAuth string) error {
	if ca.Fail {
		return fmt.Errorf("refused by the test")
	}
	switch typ {
	case "http-01":
		cl := &http.Client{Timeout: 5 * time.Second}
		res, err := cl.Get("http://" + ca.HTTPTarget + "/.well-known/acme-challenge/" + token)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		if res.StatusCode != 200 || strings.TrimSpace(string(b)) != keyAuth {
			return fmt.Errorf("http-01 answered %d %q", res.StatusCode, b)
		}
		return nil
	case "tls-alpn-01":
		c, err := tls.Dial("tcp", ca.ALPNTarget, &tls.Config{ServerName: name, NextProtos: []string{acme.ALPNProto}, InsecureSkipVerify: true})
		if err != nil {
			return err
		}
		defer c.Close()
		st := c.ConnectionState()
		if st.NegotiatedProtocol != acme.ALPNProto || len(st.PeerCertificates) != 1 {
			return fmt.Errorf("tls-alpn-01: protocol %q, %d certificates", st.NegotiatedProtocol, len(st.PeerCertificates))
		}
		leaf := st.PeerCertificates[0]
		if !slices.Equal(leaf.DNSNames, []string{name}) {
			return fmt.Errorf("tls-alpn-01: names %v", leaf.DNSNames)
		}
		want := sha256.Sum256([]byte(keyAuth))
		oid := asn1.ObjectIdentifier{1, 3, 6, 1, 5, 5, 7, 1, 31}
		for _, x := range leaf.Extensions {
			if x.Id.Equal(oid) {
				var got []byte
				if _, err := asn1.Unmarshal(x.Value, &got); err != nil || !bytes.Equal(got, want[:]) || !x.Critical {
					return fmt.Errorf("tls-alpn-01: the acmeIdentifier does not match")
				}
				return nil
			}
		}
		return fmt.Errorf("tls-alpn-01: no acmeIdentifier extension")
	}
	return fmt.Errorf("unknown challenge %s", typ)
}
