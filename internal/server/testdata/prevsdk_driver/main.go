// The driver of the previous-client test (internal/server prevsdk_test.go). It is built with the client library of the
// baseline release (../prevsdk) and drives a server through the calls a script or an application makes: read the
// server, create a jail and wait, read it with its ETag, list, change it with If-Match (a stale tag must be a
// conflict), start, read the state, snapshot, read operations, the host objects and the firewall, and delete. Every
// step prints "ok NAME" or "FAIL NAME: reason"; the exit status is 1 when a step failed.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

var failed bool

func step(name string, fn func() error) {
	if err := fn(); err != nil {
		failed = true
		fmt.Printf("FAIL %s: %v\n", name, err)
		return
	}
	fmt.Printf("ok %s\n", name)
}

func check(cond bool, format string, a ...any) error {
	if cond {
		return nil
	}
	return fmt.Errorf(format, a...)
}

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: driver URL TOKEN PROJECT")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c, err := sdk.Connect(ctx, sdk.Remote(os.Args[1], sdk.WithToken(os.Args[2]), sdk.WithProject(os.Args[3]), sdk.WithRetries(0)))
	if err != nil {
		fmt.Printf("FAIL connect: %v\n", err)
		os.Exit(1)
	}
	defer c.Close()

	step("info", func() error {
		info, err := c.Info(ctx)
		if err != nil {
			return err
		}
		if err := check(info.APIVersion == "1.0", "api_version %q", info.APIVersion); err != nil {
			return err
		}
		for _, want := range []string{"operations", "etag", "jails", "projects", "firewall_anchors"} {
			if !c.Has(want) {
				return fmt.Errorf("extension %q is missing", want)
			}
		}
		return nil
	})

	jail := api.Jail{Metadata: api.ObjectMeta{Name: "web"}, Spec: api.JailSpec{
		Image: "fb", Storage: "default",
		Update:  api.UpdatePolicy{Strategy: api.UpdateRebase, Healthcheck: "/bin/true"},
		Network: []api.NetworkAttach{{Network: "lan", IPv4: "auto"}},
	}}
	step("jail create and wait", func() error {
		op, err := c.Jails().Create(ctx, jail)
		if err != nil {
			return err
		}
		j, err := op.Wait(ctx)
		if err != nil {
			return err
		}
		return check(j.Metadata.Name == "web" && j.Metadata.Generation >= 1, "created %+v", j.Metadata)
	})
	var etag string
	var gen int64
	step("jail get with its etag", func() error {
		j, tag, err := c.Jails().GetETag(ctx, "web")
		if err != nil {
			return err
		}
		etag, gen = tag, j.Metadata.Generation
		return check(tag != "" && j.Spec.Image == "fb", "etag %q spec %+v", tag, j.Spec)
	})
	step("jail list", func() error {
		for j, err := range c.Jails().List(ctx) {
			if err != nil {
				return err
			}
			if j.Metadata.Name == "web" {
				return nil
			}
		}
		return errors.New("web is not in the list")
	})
	step("jail patch with the current etag", func() error {
		j, err := c.Jails().Patch(ctx, "web", map[string]any{"spec": map[string]any{"comment": "the site"}}, etag)
		if err != nil {
			return err
		}
		return check(j.Spec.Comment == "the site" && j.Metadata.Generation > gen, "patched %+v", j.Metadata)
	})
	step("jail patch with a stale etag is a conflict", func() error {
		_, err := c.Jails().Patch(ctx, "web", map[string]any{"spec": map[string]any{"comment": "other"}}, etag)
		return check(api.IsCode(err, api.CodeConflict), "want conflict, got %v", err)
	})
	step("jail patch with an unknown member is invalid", func() error {
		_, err := c.Jails().Patch(ctx, "web", map[string]any{"spec": map[string]any{"coment": "typo"}}, "")
		return check(api.IsCode(err, api.CodeInvalid), "want invalid, got %v", err)
	})
	step("jail get of a missing jail is not found", func() error {
		_, err := c.Jails().Get(ctx, "nope")
		return check(api.IsCode(err, api.CodeNotFound), "want not_found, got %v", err)
	})
	step("jail start, state, stop", func() error {
		op, err := c.Jails().Start(ctx, "web")
		if err != nil {
			return err
		}
		if _, err := op.Wait(ctx); err != nil {
			return err
		}
		st, err := c.Jails().State(ctx, "web")
		if err != nil {
			return err
		}
		if err := check(st.Power == api.PowerRunning, "power %q", st.Power); err != nil {
			return err
		}
		op, err = c.Jails().Stop(ctx, "web")
		if err != nil {
			return err
		}
		_, err = op.Wait(ctx)
		return err
	})
	step("snapshot create, list, delete", func() error {
		op, err := c.Jails().Snapshots("web").Create(ctx, "s1", "")
		if err != nil {
			return err
		}
		if _, err := op.Wait(ctx); err != nil {
			return err
		}
		found := false
		for s, err := range c.Jails().Snapshots("web").List(ctx) {
			if err != nil {
				return err
			}
			found = found || s.Metadata.Name == "s1"
		}
		if !found {
			return errors.New("s1 is not listed")
		}
		del, err := c.Jails().Snapshots("web").Delete(ctx, "s1")
		if err != nil {
			return err
		}
		_, err = del.Wait(ctx)
		return err
	})
	step("operations list and get", func() error {
		var id string
		for o, err := range c.Operations().List(ctx) {
			if err != nil {
				return err
			}
			id = o.ID
			break
		}
		if id == "" {
			return errors.New("no operation is listed")
		}
		o, err := c.Operations().Get(ctx, id)
		if err != nil {
			return err
		}
		return check(o.ID == id && o.Status.Final(), "operation %+v", o)
	})
	step("host objects", func() error {
		if _, err := c.Networks().Get(ctx, "lan"); err != nil {
			return err
		}
		for _, err := range c.Networks().List(ctx) {
			if err != nil {
				return err
			}
		}
		for _, err := range c.Networks().Leases(ctx, "lan") {
			if err != nil {
				return err
			}
		}
		for _, err := range c.Storage().List(ctx) {
			if err != nil {
				return err
			}
		}
		if _, err := c.Images().Get(ctx, "fb"); err != nil {
			return err
		}
		for _, err := range c.Images().Generations(ctx, "fb") {
			if err != nil {
				return err
			}
		}
		_, err := c.Projects().Get(ctx, os.Args[3])
		return err
	})
	step("firewall policy and address set", func() error {
		set := api.AddressSet{Metadata: api.ObjectMeta{Name: "office"}, Spec: api.AddressSetSpec{Addresses: []api.AddressEntry{{Address: "192.0.2.0/24"}}}}
		if _, err := c.Firewall().AddressSets().Create(ctx, set); err != nil {
			return err
		}
		pol := api.FirewallPolicy{Metadata: api.ObjectMeta{Name: "ssh"}, Spec: api.FirewallPolicySpec{Rules: []api.FirewallRule{
			{Action: "allow", Direction: "in", Proto: "tcp", From: []string{"set:office"}, Ports: []string{"22"}}}}}
		if _, err := c.Firewall().Policies().Create(ctx, pol); err != nil {
			return err
		}
		got, tag, err := c.Firewall().Policies().GetETag(ctx, "ssh")
		if err != nil {
			return err
		}
		if err := check(len(got.Spec.Rules) == 1 && tag != "", "policy %+v etag %q", got.Spec, tag); err != nil {
			return err
		}
		if _, err := c.Firewall().Policies().Patch(ctx, "ssh", map[string]any{"spec": map[string]any{"comment": "the office"}}, tag); err != nil {
			return err
		}
		for _, err := range c.Firewall().Policies().List(ctx) {
			if err != nil {
				return err
			}
		}
		res, err := c.Firewall().Test(ctx, api.FirewallTestRequest{From: "192.0.2.7", To: "jail:web", Proto: "tcp", Port: 22})
		if err != nil {
			return err
		}
		_ = res
		return nil
	})
	step("jail reach", func() error {
		r, err := c.Jails().Reach(ctx, "web")
		if err != nil {
			return err
		}
		return check(r.Jail == "web" && len(r.Rows) > 0, "reach %+v", r)
	})
	step("jail delete and wait", func() error {
		op, err := c.Jails().Delete(ctx, "web")
		if err != nil {
			return err
		}
		_, err = op.Wait(ctx)
		return err
	})
	if failed {
		os.Exit(1)
	}
}
