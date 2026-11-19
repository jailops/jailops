package sdk_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

// A client reads the enum values of a later server: a power state, an operation status and action, a condition status
// and an error code it has never heard of come through as the strings they are, and nothing fails on them.
func TestClientReadsUnknownEnumValues(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1":
			_, _ = w.Write([]byte(`{"type":"sync","status":"Success","status_code":200,"metadata":{"api_version":"1.0","api_extensions":["jails"],"version":"9.9.9","host_uuid":"u","hostname":"h","role":"host","auth":"trusted","environment":{}}}`))
		case "/api/v1/jails":
			_, _ = w.Write([]byte(`{"type":"sync","status":"Success","status_code":200,"metadata":[{"metadata":{"name":"web"},"spec":{"image":"freebsd-15"},"status":{"power":"hibernating","conditions":[{"type":"FromTheFuture","status":"Maybe","reason":"Whatever","message":"m"}],"shell_setup":{"state":"paused"}}}]}`))
		case "/api/v1/operations/op1":
			_, _ = w.Write([]byte(`{"type":"sync","status":"Success","status_code":200,"metadata":{"id":"op1","action":"jail.teleport","description":"d","status":"paused","may_cancel":false,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}}`))
		default:
			w.WriteHeader(http.StatusTeapot)
			_, _ = w.Write([]byte(`{"type":"error","status":"Failure","status_code":418,"error":{"code":"from_the_future","message":"no"}}`))
		}
	}))
	t.Cleanup(ts.Close)
	c, err := sdk.Connect(ctxT(t), sdk.Remote(ts.URL), sdk.WithBackoff(time.Millisecond, 5*time.Millisecond), sdk.WithProject("web"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	for j, err := range c.Jails().List(ctxT(t)) {
		if err != nil {
			t.Fatalf("a jail with values this client does not know: %v", err)
		}
		if j.Status.Power != "hibernating" || j.Status.Conditions[0].Status != "Maybe" || j.Status.ShellSetup.State != "paused" {
			t.Errorf("the values are kept as they came: %+v", j.Status)
		}
	}
	op, err := c.Operations().Get(ctxT(t), "op1")
	if err != nil || op.Action != "jail.teleport" || op.Status != "paused" || op.Status.Final() {
		t.Errorf("an operation with an action and a status this client does not know: %+v %v", op, err)
	}
	_, err = c.Operations().Get(ctxT(t), "op2")
	if !api.IsCode(err, "from_the_future") {
		t.Errorf("an error code this client does not know keeps its code: %v", err)
	}
}
