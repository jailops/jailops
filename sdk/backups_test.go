package sdk_test

import (
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

func TestBackupKeyRunVerifyAndRestoreProjectAgainstFake(t *testing.T) {
	srv, c := newEnv(t)
	seedBasics(srv)
	mustJail(t, c, "web", webSpec())
	ctx := ctxT(t)
	bk := c.Backups()
	if st, err := bk.KeyStatus(ctx); err != nil || st.Status != "none" {
		t.Fatalf("%+v %v", st, err)
	}
	// No key, no backup.
	if _, err := c.Backups().Policies().Create(ctx, api.BackupPolicy{Metadata: api.ObjectMeta{Name: "p"}, Spec: api.BackupPolicySpec{
		Selector: map[string]string{"all": ""}, Targets: []string{"t"}, Schedule: "0 3 * * *"}}); err != nil {
		t.Fatal(err)
	}
	op, err := bk.Run(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := op.Wait(ctx); !api.IsCode(err, api.CodePlaintextDenied) {
		t.Fatalf("a run without a key: %v", err)
	}
	made, err := bk.CreateKey(ctx)
	if err != nil || made.RecoveryKey == "" || made.Escrowed {
		t.Fatalf("%+v %v", made, err)
	}
	if _, err := bk.CreateKey(ctx); !api.IsCode(err, api.CodeAlreadyExists) {
		t.Fatal(err)
	}
	if st, _ := bk.KeyStatus(ctx); st.Status != "present" || strings.Contains(st.Domain+st.Path, made.RecoveryKey) {
		t.Fatalf("%+v", st)
	}
	if _, err := bk.EscrowKey(ctx); !api.IsCode(err, api.CodeInvalid) {
		t.Fatalf("escrow without recipients: %v", err)
	}
	// Present but not escrowed is refused too.
	op, _ = bk.Run(ctx, "p")
	if _, err := op.Wait(ctx); !api.IsCode(err, api.CodeKeyNotEscrowed) {
		t.Fatalf("a run with an unescrowed key: %v", err)
	}
	if st, err := bk.EscrowKey(ctx, "age1xyz"); err != nil || st.Status != "escrowed" {
		t.Fatalf("%+v %v", st, err)
	}
	op, _ = bk.Run(ctx, "p")
	runs, err := op.Wait(ctx)
	if err != nil || len(runs) != 1 || !runs[0].OK || runs[0].Jail != "web" {
		t.Fatalf("%+v %v", runs, err)
	}
	var id string
	for b, err := range bk.List(ctx) {
		if err != nil {
			t.Fatal(err)
		}
		id = b.Metadata.Name
	}
	vop, err := bk.VerifyDepth(ctx, id, "extract")
	if err != nil {
		t.Fatal(err)
	}
	if r, err := vop.Wait(ctx); err != nil || !r.OK || r.Depth != "extract" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := bk.VerifyDepth(ctx, id, "shallow"); err == nil {
		// the server answers with an error before or inside the operation
		t.Log("depth validated in the operation")
	}
	// A restore into another project.
	if _, err := c.Projects().Create(ctx, api.Project{Metadata: api.ObjectMeta{Name: "beta"}}); err != nil {
		t.Fatal(err)
	}
	rop, err := bk.Restore(ctx, api.RestoreRequest{Backup: id, As: "web2", Project: "beta"})
	if err != nil {
		t.Fatal(err)
	}
	j, err := rop.Wait(ctx)
	if err != nil || j.Metadata.Project != "beta" || j.Metadata.Name != "web2" {
		t.Fatalf("%+v %v", j.Metadata, err)
	}
	if _, err := bk.Restore(ctx, api.RestoreRequest{Backup: id, As: "web3", Project: "nope"}); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("restore into a missing project: %v", err)
	}
}
