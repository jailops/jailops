package sdk_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"time"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
	"jailops.net/jailops/sdk/fake"
)

func exampleClient() (*sdk.Client, func()) {
	srv := fake.New()
	seedBasics(srv)
	ts := httptest.NewServer(srv)
	c, err := sdk.Connect(context.Background(), sdk.Remote(ts.URL), sdk.WithProject("web"))
	if err != nil {
		panic(err)
	}
	return c, func() { c.Close(); ts.Close(); srv.Close() }
}

// Create a jail, wait for the operation and read the typed result.
func Example() {
	c, done := exampleClient()
	defer done()
	ctx := context.Background()

	op, err := c.Jails().Create(ctx, sdk.NewJail("web", webSpec()))
	if err != nil {
		panic(err)
	}
	j, err := op.Wait(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println(j.Metadata.Name, j.Status.Power, j.Spec.Network[0].IPv4)
	// Output: web stopped 10.20.0.10
}

// Start a jail and list jails by label.
func ExampleJailClient_List() {
	c, done := exampleClient()
	defer done()
	ctx := context.Background()

	for _, env := range []string{"prod", "dev"} {
		j := sdk.NewJail("app-"+env, webSpec())
		j.Metadata.Labels = map[string]string{"env": env}
		op, _ := c.Jails().Create(ctx, j)
		if _, err := op.Wait(ctx); err != nil {
			panic(err)
		}
	}
	for j, err := range c.Jails().List(ctx, sdk.Label("env", "prod")) {
		if err != nil {
			panic(err)
		}
		fmt.Println(j.Metadata.Name)
	}
	// Output: app-prod
}

// Modify reads with the ETag, applies the change and retries on conflict.
func ExampleModify() {
	c, done := exampleClient()
	defer done()
	ctx := context.Background()

	op, _ := c.Jails().Create(ctx, sdk.NewJail("web", webSpec()))
	if _, err := op.Wait(ctx); err != nil {
		panic(err)
	}
	err := sdk.Modify(ctx, c.Jails(), "web", func(j *api.Jail) {
		j.Spec.Boot.Autostart = true
	})
	j, _ := c.Jails().Get(ctx, "web")
	fmt.Println(err, j.Spec.Boot.Autostart, j.Metadata.Generation)
	// Output: <nil> true 2
}

// Errors are *api.Error and work with errors.As, errors.Is and api.IsCode.
func ExampleClient_Has() {
	c, done := exampleClient()
	defer done()
	_, err := c.Jails().Get(context.Background(), "missing")
	fmt.Println(api.IsCode(err, api.CodeNotFound), c.Has("jail_snapshots"), c.Has("nope"))
	// Output: true true false
}

// Events streams server-sent events and reconnects on its own.
func ExampleClient_Events() {
	srv := fake.New()
	seedBasics(srv)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	defer srv.Close()
	c, _ := sdk.Connect(context.Background(), sdk.Remote(ts.URL)) // events and host-level kinds need no project
	defer c.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan api.LifecycleEvent, 1)
	go func() {
		for ev, err := range c.Events(ctx, sdk.Types(api.EventLifecycle)) {
			if err != nil {
				return
			}
			var le api.LifecycleEvent
			_ = jsonUnmarshal(ev.Metadata, &le)
			got <- le
			return
		}
	}()
	for srv.EventStreams() == 0 {
		time.Sleep(time.Millisecond)
	}
	_, _ = c.Networks().Create(ctx, api.Network{
		Metadata: api.ObjectMeta{Name: "dmz"},
		Spec:     api.NetworkSpec{IPv4: &api.IPv4Net{Subnet: "10.9.0.0/24", Gateway: "10.9.0.1", Range: "10.9.0.2-10.9.0.9"}},
	})
	le := <-got
	fmt.Println(le.Action, le.Source)
	// Output: network-created /api/v1/networks/dmz
}
