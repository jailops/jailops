package sdk

import (
	"context"
	"net/http"

	"jailops.net/jailops/sdk/api"
)

// CertificateClient is the certificate of the console and the API (extension "host_certificate").
type CertificateClient struct{ c *Client }

// Certificate returns the client of the console's certificate.
func (h *HostClient) Certificate() *CertificateClient { return &CertificateClient{h.c} }

// Get describes the certificate the TLS listener serves, where it comes from and, with ACME, its renewal
// (GET /api/v1/host/certificate).
func (cc *CertificateClient) Get(ctx context.Context) (api.HostCertificate, error) {
	if err := cc.c.need("host_certificate"); err != nil {
		return api.HostCertificate{}, err
	}
	v, _, err := get[api.HostCertificate](ctx, cc.c, "/api/v1/host/certificate", nil)
	return v, err
}

// Upload makes the listener serve a certificate chain and key (PEM) from the next handshake on
// (PUT /api/v1/host/certificate, admin).
func (cc *CertificateClient) Upload(ctx context.Context, in api.HostCertificateUpload) (api.HostCertificate, error) {
	return cc.send(ctx, http.MethodPut, "/api/v1/host/certificate", in)
}

// UseACME turns ACME on with spec and starts a request at once (POST /api/v1/host/certificate/acme, admin). The
// answer comes before the CA does: Get follows the request.
func (cc *CertificateClient) UseACME(ctx context.Context, spec api.ACMESpec) (api.HostCertificate, error) {
	return cc.send(ctx, http.MethodPost, "/api/v1/host/certificate/acme", spec)
}

// UseSelfSigned goes back to the daemon's generated certificate (POST /api/v1/host/certificate/self-signed, admin).
func (cc *CertificateClient) UseSelfSigned(ctx context.Context) (api.HostCertificate, error) {
	return cc.send(ctx, http.MethodPost, "/api/v1/host/certificate/self-signed", struct{}{})
}

func (cc *CertificateClient) send(ctx context.Context, method, path string, body any) (api.HostCertificate, error) {
	if err := cc.c.need("host_certificate"); err != nil {
		return api.HostCertificate{}, err
	}
	v, _, err := send[api.HostCertificate](ctx, cc.c, request{method: method, path: path, body: body})
	return v, err
}
