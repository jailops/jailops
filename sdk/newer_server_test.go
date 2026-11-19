package sdk_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"jailops.net/jailops/sdk"
)

// A client reads what a later server sends: members it does not know are ignored in the server information, in a
// list and in an object, and an extension it does not know changes nothing. A request for an extension the server
// does not list fails fast with ErrUnsupported instead of reaching the server.
func TestClientReadsAnswersOfALaterServer(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1":
			_, _ = w.Write([]byte(`{"type":"sync","status":"Success","status_code":200,"a_new_envelope_member":1,"metadata":{"api_version":"1.0","api_extensions":["jails","from_the_future"],"version":"9.9.9","host_uuid":"u","hostname":"h","role":"host","auth":"trusted","environment":{},"a_new_member":{"x":1}}}`))
		case "/api/v1/jails":
			_, _ = w.Write([]byte(`{"type":"sync","status":"Success","status_code":200,"metadata":[{"metadata":{"name":"web","a_new_metadata_member":1},"spec":{"image":"freebsd-15","a_new_spec_member":"x"},"status":{"power":"running","a_new_status_member":[1,2]},"a_new_member":true}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	c, err := sdk.Connect(ctxT(t), sdk.Remote(ts.URL), sdk.WithBackoff(time.Millisecond, 5*time.Millisecond), sdk.WithProject("web"))
	if err != nil {
		t.Fatalf("a later server's information was refused: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if !c.Has("from_the_future") || c.Has("never_heard_of") {
		t.Fatal("extensions are read as the server lists them")
	}
	n := 0
	for j, err := range c.Jails().List(ctxT(t)) {
		if err != nil {
			t.Fatalf("a list with members this client does not know: %v", err)
		}
		if j.Metadata.Name != "web" {
			t.Fatalf("%+v", j)
		}
		n++
	}
	if n != 1 {
		t.Fatalf("%d jails", n)
	}
	if err := c.Require(ctxT(t), "never_heard_of"); !errors.Is(err, sdk.ErrUnsupported) {
		t.Fatalf("an extension the server does not list: %v", err)
	}
}
