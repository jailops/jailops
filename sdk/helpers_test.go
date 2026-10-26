package sdk_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
	"jailops.net/jailops/sdk/fake"
)

func ctxT(t testing.TB) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// seedProject creates the project the tests work in: the server has no default project.
func seedProject(s *fake.Server) {
	s.SeedProject(api.Project{Metadata: api.ObjectMeta{Name: "web"}, Spec: api.ProjectSpec{
		Networks: []api.ProjectNetwork{{Network: "lan"}}, Storage: []api.ProjectStorage{{Storage: "default"}}}})
}

func seedBasics(s *fake.Server) {
	seedProject(s)
	s.SeedImage(api.Image{
		Metadata: api.ObjectMeta{Name: "freebsd-15"},
		Spec:     api.ImageSpec{OS: api.OSFreeBSD, Release: "15.0-RELEASE", Arch: "amd64", Source: api.ImageSource{Type: "pkgbase"}, Aliases: []string{"stable"}},
	})
	s.SeedGeneration("freebsd-15")
	s.SeedNetwork(api.Network{
		Metadata: api.ObjectMeta{Name: "lan"},
		Spec: api.NetworkSpec{IPv4: &api.IPv4Net{
			Subnet: "10.20.0.0/24", Gateway: "10.20.0.1", Range: "10.20.0.10-10.20.0.12",
		}},
	})
}

func newEnv(t *testing.T, fopts ...fake.Option) (*fake.Server, *sdk.Client) {
	t.Helper()
	srv := fake.New(fopts...)
	seedProject(srv)
	ts := httptest.NewServer(srv)
	c, err := sdk.Connect(ctxT(t), sdk.Remote(ts.URL), sdk.WithBackoff(time.Millisecond, 5*time.Millisecond), sdk.WithProject("web"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(); ts.Close(); srv.Close() })
	return srv, c
}

func webSpec() api.JailSpec {
	return api.JailSpec{
		Image:   "freebsd-15@stable",
		Storage: "default",
		Network: []api.NetworkAttach{{Network: "lan", IPv4: "auto"}},
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
