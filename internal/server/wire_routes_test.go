package server_test

import (
	"testing"
	"time"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk/api"
)

const webBody = `{"metadata":{"name":"web"},"spec":{"image":"fb","storage":"default","update":{"healthcheck":"/bin/true"},"network":[{"network":"lan","ipv4":"auto"}]}}`

// settled waits until the background setup of the shell of a new jail is over, so that the jail a transcript shows is
// the same one every time (the setup is the daemon's own work and its timing is not part of the contract).
func settled(t *testing.T, e *dtest.Env, name string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		j, err := e.Admin.Jails().Get(ctxT(t), name)
		if err != nil {
			t.Fatal(err)
		}
		busy := false
		for _, c := range j.Status.Conditions {
			if c.Type == "ShellSetup" && c.Status == api.ConditionUnknown {
				busy = true
			}
		}
		if !busy {
			return
		}
		time.Sleep(10 * time.Millisecond) // sleep-ok: the poll of a condition
	}
	t.Fatalf("the shell setup of %s did not finish", name)
}

// The life of a jail on the wire: create (asynchronous), read with its ETag, change with If-Match (a stale tag is
// refused with 412), start and stop through the state, the list, the errors a client meets and the delete.
func TestFreezeWireJails(t *testing.T) {
	e := dtest.New(t, dtest.Options{})
	e.Seed()
	w := newWire(t, e)

	w.note("create: the request is accepted with 202 and the Location of the operation; the operation ends with the jail")
	r := w.do("POST", "/api/v1/jails", webBody)
	w.wait(r.opID())
	settled(t, e, "web")

	w.note("read: the ETag is the generation, the object carries metadata, spec and status")
	g := w.do("GET", "/api/v1/jails/web", "")
	etag := g.etag()

	w.note("list")
	w.do("GET", "/api/v1/jails", "")

	w.note("a merge patch with the current tag succeeds and answers the new tag")
	w.do("PATCH", "/api/v1/jails/web", `{"spec":{"comment":"the site"}}`, "If-Match", etag)

	w.note("the same tag again is stale: 412 conflict")
	w.do("PATCH", "/api/v1/jails/web", `{"spec":{"comment":"other"}}`, "If-Match", etag)

	w.note("a member the type does not have is invalid, and the message names it")
	w.do("PATCH", "/api/v1/jails/web", `{"spec":{"coment":"typo"}}`)

	w.note("the state: start is asynchronous, the state is read from its own route")
	s := w.do("PUT", "/api/v1/jails/web/state", `{"action":"start"}`)
	w.wait(s.opID())
	w.do("GET", "/api/v1/jails/web/state", "")

	w.note("a start of a running jail is accepted and finishes without a change: the call is idempotent")
	again := w.do("PUT", "/api/v1/jails/web/state", `{"action":"start"}`)
	w.wait(again.opID())

	w.note("errors: not found, an invalid name, an unknown route")
	w.do("GET", "/api/v1/jails/nope", "")
	w.do("POST", "/api/v1/jails", `{"metadata":{"name":"Bad_Name"},"spec":{"image":"fb"}}`)
	w.do("GET", "/api/v1/nothing", "")

	w.note("stop and delete: delete is asynchronous too")
	st := w.do("PUT", "/api/v1/jails/web/state", `{"action":"stop"}`)
	w.wait(st.opID())
	d := w.do("DELETE", "/api/v1/jails/web", "")
	w.wait(d.opID())
	w.do("GET", "/api/v1/jails", "")

	w.check("jails")
}

// The host objects: server info, networks, storage, images and projects.
func TestFreezeWireHostObjects(t *testing.T) {
	e := dtest.New(t, dtest.Options{})
	e.Seed()
	w := newWire(t, e)

	w.note("the server info with an identity")
	w.do("GET", "/api/v1", "")

	w.note("networks, storage and images are host objects: no project in the request")
	w.do("GET", "/api/v1/networks", "")
	n := w.do("GET", "/api/v1/networks/lan", "")
	w.do("GET", "/api/v1/networks/lan/leases", "")
	w.do("GET", "/api/v1/storage", "")
	w.do("GET", "/api/v1/images", "")
	w.do("GET", "/api/v1/images/fb", "")
	_ = n

	w.note("projects: read, create (synchronous, 201), change with If-Match, delete")
	w.do("GET", "/api/v1/projects", "")
	w.do("GET", "/api/v1/projects/alpha", "")
	c := w.do("POST", "/api/v1/projects", `{"metadata":{"name":"beta"},"spec":{}}`)
	g := w.do("GET", "/api/v1/projects/beta", "")
	w.do("PATCH", "/api/v1/projects/beta", `{"spec":{"comment":"second"}}`, "If-Match", g.etag())
	w.do("DELETE", "/api/v1/projects/beta", "")
	_ = c

	w.note("without a credential, and with a name that does not exist")
	w.token = ""
	w.do("GET", "/api/v1/jails", "")
	w.token = e.Token(api.PermAdmin)
	w.do("GET", "/api/v1/projects/nope", "")

	w.check("host_objects")
}

// Firewall policies and address sets, the test of a packet and the reach of a jail.
func TestFreezeWireFirewall(t *testing.T) {
	e := dtest.New(t, dtest.Options{})
	e.Seed()
	w := newWire(t, e)
	r := w.do("POST", "/api/v1/jails", webBody)
	w.wait(r.opID())
	settled(t, e, "web")

	w.note("an address set and a policy: create (201), read, change with If-Match, list")
	w.do("POST", "/api/v1/firewall/address-sets", `{"metadata":{"name":"office"},"spec":{"addresses":[{"address":"192.0.2.0/24"}]}}`)
	w.do("POST", "/api/v1/firewall/policies", `{"metadata":{"name":"ssh"},"spec":{"rules":[{"action":"allow","direction":"in","proto":"tcp","from":["set:office"],"ports":["22"]}]}}`)
	g := w.do("GET", "/api/v1/firewall/policies/ssh", "")
	w.do("PATCH", "/api/v1/firewall/policies/ssh", `{"spec":{"comment":"the office reaches ssh"}}`, "If-Match", g.etag())
	w.do("GET", "/api/v1/firewall/policies", "")
	w.do("GET", "/api/v1/firewall/address-sets", "")

	w.note("a policy a jail uses, then what reaches the jail and what the test says about one packet")
	cfg := w.do("POST", "/api/v1/jails/web/config", `{"set":{"firewall.policies":"ssh"}}`)
	w.wait(cfg.opID())
	w.do("GET", "/api/v1/jails/web/reach", "")
	w.do("POST", "/api/v1/firewall/test", `{"from":"192.0.2.7","to":"jail:web","proto":"tcp","port":22}`)

	w.note("a policy in use is not deleted")
	w.do("DELETE", "/api/v1/firewall/policies/ssh", "")

	w.check("firewall")
}

// Operations: the list, one record, the wait and the cancel of a finished one.
func TestFreezeWireOperations(t *testing.T) {
	e := dtest.New(t, dtest.Options{})
	e.Seed()
	w := newWire(t, e)
	r := w.do("POST", "/api/v1/jails", webBody)
	id := r.opID()
	w.wait(id)
	settled(t, e, "web")

	w.note("the list of operations and one record")
	w.do("GET", "/api/v1/operations", "")
	w.do("GET", "/api/v1/operations/"+id, "")

	w.note("a wait on a finished operation answers at once; an unknown id is not found")
	w.do("GET", "/api/v1/operations/"+id+"/wait?timeout=1", "")
	w.do("GET", "/api/v1/operations/nope", "")

	w.check("operations")
}
