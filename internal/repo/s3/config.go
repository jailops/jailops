package s3

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

// Credentials are static access keys. The printed forms are redacted so that a
// credential in a log line or an error never leaks.
type Credentials struct {
	AccessKey    string
	SecretKey    string
	SessionToken string
}

// String implements fmt.Stringer without the secrets.
func (c Credentials) String() string {
	if c.AccessKey == "" {
		return "s3.Credentials{}"
	}
	return "s3.Credentials{AccessKey:" + c.AccessKey + ", SecretKey:redacted}"
}

// GoString implements fmt.GoStringer without the secrets.
func (c Credentials) GoString() string { return c.String() }

// MarshalJSON refuses to serialise the secrets.
func (c Credentials) MarshalJSON() ([]byte, error) {
	return []byte(`{"access_key":"` + c.AccessKey + `","secret_key":"redacted"}`), nil
}

// LogValue keeps the secrets out of structured logs.
func (c Credentials) LogValue() slog.Value { return slog.StringValue(c.String()) }

// Addressing selects how the bucket is named in a request.
type Addressing string

// Addressing styles.
const (
	// AddressingAuto uses virtual-host style for the default AWS endpoint and
	// path style for any endpoint override.
	AddressingAuto Addressing = ""
	// AddressingPath puts the bucket in the path: https://host/bucket/key.
	AddressingPath Addressing = "path"
	// AddressingVirtual puts the bucket in the host name: https://bucket.host/key.
	AddressingVirtual Addressing = "virtual"
)

// PayloadSigning selects what x-amz-content-sha256 carries on a PUT.
type PayloadSigning string

// Payload signing modes.
const (
	// PayloadAuto signs the payload on plain HTTP and leaves it unsigned on TLS.
	PayloadAuto PayloadSigning = ""
	// PayloadAlways always computes the SHA-256 of the body.
	PayloadAlways PayloadSigning = "always"
	// PayloadNever always sends UNSIGNED-PAYLOAD.
	PayloadNever PayloadSigning = "never"
)

// Config is everything needed to reach an S3-compatible bucket.
type Config struct {
	// Endpoint is host[:port] or a URL with a scheme; empty means AWS
	// (s3.<region>.amazonaws.com over TLS).
	Endpoint string
	Region   string
	Bucket   string
	// Prefix is a key prefix inside the bucket that holds the repository.
	Prefix      string
	Credentials Credentials
	Addressing  Addressing
	// NoTLS selects plain HTTP when Endpoint has no scheme.
	NoTLS bool
	// CAFile or CAPEM trusts a private CA for the endpoint. InsecureSkipVerify
	// is a last resort and should be left false.
	CAFile             string
	CAPEM              string
	InsecureSkipVerify bool
	Payload            PayloadSigning

	// Attempts is the number of tries per request (default 6). BackoffBase and
	// BackoffMax bound the jittered exponential delay between tries (default
	// 100 ms and 5 s).
	Attempts    int
	BackoffBase time.Duration
	BackoffMax  time.Duration
	// StallTimeout aborts a request or a body read that makes no progress for
	// this long (default 60 s).
	StallTimeout time.Duration
	// MaxConns bounds the idle and per-host connections kept (default 128).
	MaxConns int
	// MaxBufferedPut is the largest non-seekable body Put will buffer to be
	// able to retry it (default 256 MiB). Seekable bodies are never buffered.
	MaxBufferedPut int64
	// UserAgent is sent on every request (default "jailops-s3").
	UserAgent string

	// HTTPClient replaces the transport entirely (tests). When set, the TLS and
	// pool settings above are ignored.
	HTTPClient *http.Client
	// Now replaces the clock used for signing (tests).
	Now func() time.Time
	// Sleep replaces the backoff wait (tests); it must honour ctx.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Validate reports a configuration problem without touching the network.
func (c Config) Validate() error {
	switch {
	case c.Bucket == "":
		return errors.New("s3: bucket is required")
	case c.Credentials.AccessKey == "" || c.Credentials.SecretKey == "":
		return errors.New("s3: access key and secret key are required")
	case c.Endpoint == "" && c.Region == "":
		return errors.New("s3: region is required when no endpoint is given")
	}
	if strings.ContainsAny(c.Bucket, "/ \t\r\n") {
		return fmt.Errorf("s3: invalid bucket name %q", c.Bucket)
	}
	switch c.Addressing {
	case AddressingAuto, AddressingPath, AddressingVirtual:
	default:
		return fmt.Errorf("s3: unknown addressing style %q", c.Addressing)
	}
	switch c.Payload {
	case PayloadAuto, PayloadAlways, PayloadNever:
	default:
		return fmt.Errorf("s3: unknown payload signing mode %q", c.Payload)
	}
	return nil
}

// String describes the target without any secret.
func (c Config) String() string {
	return fmt.Sprintf("s3://%s/%s (endpoint %q, region %q, %s)", c.Bucket, strings.Trim(c.Prefix, "/"), c.Endpoint, c.Region, c.Credentials)
}

// target is the resolved endpoint.
type target struct {
	scheme  string // http or https
	host    string // host[:port] of the service endpoint
	virtual bool
}

func resolveTarget(c Config) (target, error) {
	ep := strings.TrimRight(c.Endpoint, "/")
	t := target{scheme: "https"}
	if c.NoTLS {
		t.scheme = "http"
	}
	switch {
	case ep == "":
		region := c.Region
		if region == "" {
			region = "us-east-1"
		}
		t.host = "s3." + region + ".amazonaws.com"
		t.virtual = c.Addressing != AddressingPath
		return t, nil
	case strings.HasPrefix(ep, "https://"):
		t.scheme, t.host = "https", strings.TrimPrefix(ep, "https://")
	case strings.HasPrefix(ep, "http://"):
		t.scheme, t.host = "http", strings.TrimPrefix(ep, "http://")
	default:
		t.host = ep
	}
	if t.host == "" || strings.ContainsAny(t.host, "/?#@ ") {
		return t, fmt.Errorf("s3: invalid endpoint %q", c.Endpoint)
	}
	t.virtual = c.Addressing == AddressingVirtual
	return t, nil
}

func tlsConfig(c Config) (*tls.Config, error) {
	tc := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: c.InsecureSkipVerify} //nolint:gosec // explicit opt-in
	if c.CAFile != "" || c.CAPEM != "" {
		pem := []byte(c.CAPEM)
		if c.CAFile != "" {
			var err error
			if pem, err = os.ReadFile(c.CAFile); err != nil {
				return nil, fmt.Errorf("s3: ca file: %w", err)
			}
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("s3: the CA contains no certificate")
		}
		tc.RootCAs = pool
	}
	return tc, nil
}
