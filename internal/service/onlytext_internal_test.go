package service

import (
	"testing"

	"jailops.net/jailops/sdk/api"
)

// A change of the comment or the notes alone leaves the reconcile loop asleep: neither is rendered anywhere.
func TestOnlyTextChangesNeedNoReconcile(t *testing.T) {
	old := api.Jail{Metadata: api.ObjectMeta{Name: "web", Labels: map[string]string{"tier": "front"}},
		Spec: api.JailSpec{Image: "fb", Hostname: "web", Comment: "shop", Network: []api.NetworkAttach{{Network: "lan"}}}}
	notes := old
	notes.Spec.Notes, notes.Spec.Comment = "# web\nruns nginx", "the shop"
	if !onlyText(old, notes) {
		t.Error("a change of the notes and the comment kicks the reconcile loop")
	}
	host := notes
	host.Spec.Hostname = "www"
	labels := notes
	labels.Metadata.Labels = map[string]string{"tier": "back"}
	for name, nj := range map[string]api.Jail{"hostname": host, "labels": labels} {
		if onlyText(old, nj) {
			t.Errorf("a change of the %s does not kick the reconcile loop", name)
		}
	}
}
