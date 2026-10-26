package s3

import (
	"net"
	"net/http"
	"time"
)

// newTransport builds the transport a Backend owns: a connection pool sized for
// many parallel requests (the default keeps only two idle connections per
// host, which makes every request beyond that pay a new TCP and TLS
// handshake) and large HTTP/2 flow-control windows (the default caps one
// stream at a small window per round trip, which limits upload throughput on a
// long path).
func newTransport(c Config) (*http.Transport, error) {
	conns := c.MaxConns
	if conns <= 0 {
		conns = 128
	}
	tc, err := tlsConfig(c)
	if err != nil {
		return nil, err
	}
	d := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           d.DialContext,
		TLSClientConfig:       tc,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          2 * conns,
		MaxIdleConnsPerHost:   conns,
		MaxConnsPerHost:       0,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 0, // never wait for 100-continue: the body is small and the answer is the same
		ReadBufferSize:        64 << 10,
		WriteBufferSize:       64 << 10,
		DisableCompression:    true, // objects are opaque; never let the transport add Accept-Encoding
		HTTP2: &http.HTTP2Config{
			MaxReceiveBufferPerConnection: 32 << 20,
			MaxReceiveBufferPerStream:     8 << 20,
			MaxConcurrentStreams:          conns,
			SendPingTimeout:               30 * time.Second,
			PingTimeout:                   15 * time.Second,
		},
	}
	return tr, nil
}
