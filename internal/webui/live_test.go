//go:build live

package webui_test

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk/api"
)

// TestLiveConsole serves the embedded console from a real jailopsd on the fakes of internal/sys, for driving it with
// a browser (web/host/e2e-live). It is not part of any normal run: go test -tags live -run TestLiveConsole ./internal/webui
// with JAILOPS_LIVE_ADDR=127.0.0.1:8099 and JAILOPS_LIVE_SECONDS.
func TestLiveConsole(t *testing.T) {
	addr := os.Getenv("JAILOPS_LIVE_ADDR")
	if addr == "" {
		t.Skip("JAILOPS_LIVE_ADDR not set")
	}
	e := dtest.New(t, dtest.Options{ForceWeb: true}) // the console is off by default
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: e.HTTP.Config.Handler}
	go srv.Serve(ln)
	defer srv.Close()
	fmt.Printf("LIVE-URL http://%s\nLIVE-TOKEN %s\n", addr, e.Token(api.PermAdmin))
	secs, _ := strconv.Atoi(os.Getenv("JAILOPS_LIVE_SECONDS"))
	if secs == 0 {
		secs = 300
	}
	time.Sleep(time.Duration(secs) * time.Second)
}
