package service_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"jailops.net/jailops/internal/state"
	"jailops.net/jailops/sdk/api"
)

// The alias table of an image is read, named in an event for each jail that used an alias, and cleared.
func TestImageAliasTablesAreReportedAndDropped(t *testing.T) {
	e, s := newEnv(t)
	ds := s.ImageDataset("fb")
	if err := e.ZFS.Set(context.Background(), ds, map[string]string{"jailops:aliases": "stable=2,latest=3"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch := s.Events().Subscribe(ctx, -1)
	s.ReportImageAliases(context.Background(), []state.MigratedImageRef{
		{Project: "alpha", Jail: "web", Image: "fb", Alias: "stable"},
		{Project: "alpha", Jail: "db", Image: "fb", Alias: "gone"},
	})
	var details []string
	for i := 0; i < 2; i++ {
		se := <-ch
		var ev api.LifecycleEvent
		if err := json.Unmarshal(se.Event.Metadata, &ev); err != nil || ev.Action != "image-alias-removed" || ev.Project != "alpha" {
			t.Fatalf("event %+v", se)
		}
		details = append(details, ev.Source+": "+ev.Detail)
	}
	all := strings.Join(details, "\n")
	if !strings.Contains(all, "/api/v1/jails/web: jail web used the image alias fb@stable, which pointed at generation 2") ||
		!strings.Contains(all, "/api/v1/jails/db: jail db used the image alias fb@gone, which pointed at an unknown generation") {
		t.Fatalf("events:\n%s", all)
	}
	p, err := e.ZFS.Get(context.Background(), ds, "jailops:aliases")
	if err != nil {
		t.Fatal(err)
	}
	if v := p["jailops:aliases"].Value; v != "" && v != "-" {
		t.Fatalf("the table is still there: %q", v)
	}
}
