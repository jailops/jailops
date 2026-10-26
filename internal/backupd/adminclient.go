package backupd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"jailops.net/jailops/sdk/api"
)

// AdminClient talks to the administration API on the unix socket of a running daemon.
type AdminClient struct {
	hc *http.Client
}

// NewAdminClient connects lazily to the socket.
func NewAdminClient(socket string) *AdminClient {
	return &AdminClient{hc: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
		},
		DisableKeepAlives: true,
	}}}
}

// Call sends a request and decodes the metadata of the answer into out.
func (c *AdminClient) Call(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	u := url.URL{Scheme: "http", Host: "backupd", Path: path}
	if i := indexByte(path, '?'); i >= 0 {
		u.Path, u.RawQuery = path[:i], path[i+1:]
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach the backup daemon on its admin socket (is it running?): %w", err)
	}
	defer resp.Body.Close()
	var env api.Response
	if err := json.NewDecoder(io.LimitReader(resp.Body, 256<<20)).Decode(&env); err != nil {
		return fmt.Errorf("malformed answer from the backup daemon (HTTP %d): %w", resp.StatusCode, err)
	}
	if env.Type == api.ResponseError && env.Error != nil {
		return env.Error
	}
	if out != nil && len(env.Metadata) > 0 {
		return json.Unmarshal(env.Metadata, out)
	}
	return nil
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
