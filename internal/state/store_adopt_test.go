package state

import (
	"testing"

	"jailops.net/jailops/sdk/api"
)

// An object stored before projects existed joins a project once, by uuid; a
// project can never be changed after that.
func TestAdoptProjectOnce(t *testing.T) {
	st, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	put := func(uuid, project string, exp int64) error {
		_, err := st.Apply(Command{Version: CommandVersion, ID: NewUUID(), Time: t0, Op: OpPut, Kind: api.KindAddressSet,
			UUID: uuid, Name: "office", Scope: api.ScopeFleet, Project: project, Spec: []byte(`{}`), ExpectGeneration: exp})
		return err
	}
	id := NewUUID()
	if err := put(id, "", 0); err != nil {
		t.Fatal(err)
	}
	if err := put(id, "web", AnyGeneration); err != nil {
		t.Fatalf("adoption: %v", err)
	}
	if o, err := st.GetByNameIn(api.KindAddressSet, api.ScopeFleet, "web", "office"); err != nil || o.Metadata.UUID != id {
		t.Fatalf("%v %v", o, err)
	}
	if _, err := st.GetByNameIn(api.KindAddressSet, api.ScopeFleet, "", "office"); err == nil {
		t.Fatal("the old index entry survived")
	}
	if err := put(id, "db", AnyGeneration); !api.IsCode(err, api.CodeInvalid) {
		t.Fatalf("a second move must fail: %v", err)
	}
}
