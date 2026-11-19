package server_test

import (
	"encoding/json"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk/api"
)

// A list is a JSON array of objects in the metadata of the response, and a list asked with a paging parameter that
// this server does not know yet still returns everything and carries no Link header: that is the rule that lets a later
// release add opt-in paging without a client of this release losing a single object (docs/compatibility.md).
func TestAListIsWholeAndAnUnknownPagingParameterChangesNothing(t *testing.T) {
	e := seeded(t)
	tok := e.Token(api.PermAdmin)
	base := "/api/v1/firewall/address-sets?project=" + dtest.SeedProject
	for _, n := range []string{"one", "two", "three"} {
		body := `{"metadata":{"name":"` + n + `"},"spec":{"addresses":[{"address":"10.0.0.0/8"}]}}`
		if code, out := rawDo(t, e, "POST", base, tok, body, nil); code != 201 {
			t.Fatalf("%s: %d %s", n, code, out)
		}
	}
	count := func(path string) int {
		t.Helper()
		code, out := rawDo(t, e, "GET", path, tok, "", nil)
		if code != 200 {
			t.Fatalf("%s: %d %s", path, code, trunc(out))
		}
		var env struct {
			Metadata []json.RawMessage `json:"metadata"`
		}
		if err := json.Unmarshal([]byte(out), &env); err != nil {
			t.Fatalf("a list is not an array in the metadata of the response: %v: %s", err, trunc(out))
		}
		return len(env.Metadata)
	}
	all := count(base)
	if all < 3 {
		t.Fatalf("%d objects", all)
	}
	if got := count(base + "&limit=1&cursor=x"); got != all {
		t.Fatalf("a paging parameter this server does not know cut the list: %d of %d", got, all)
	}
}
