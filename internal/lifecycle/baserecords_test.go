package lifecycle

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// recordsWorld scripts the two package databases: the generation (the truth for the base set) and the jail.
type recordsWorld struct {
	w        *world
	gen, jl  []string // "name-version" lines of the two databases
	manifest map[string]string
	failInfo string // a manifest that cannot be read
	calls    []string
}

func newRecordsWorld(t *testing.T, gen, jl []string) *recordsWorld {
	r := &recordsWorld{w: newWorld(t), gen: gen, jl: jl, manifest: map[string]string{}}
	r.w.created("web")
	h := r.w.Exec.Handler
	r.w.Exec.Handler = func(argv []string) ([]byte, error) {
		if argv[0] != "pkg-records" {
			return h(argv)
		}
		r.calls = append(r.calls, strings.Join(argv[1:], " "))
		root := argv[2]
		switch argv[3] {
		case "info":
			if argv[4] == "-a" {
				if strings.Contains(root, "/images/") {
					return []byte(strings.Join(r.gen, "\n") + "\n"), nil
				}
				return []byte(strings.Join(r.jl, "\n") + "\n"), nil
			}
			n := argv[7]
			if n == r.failInfo {
				return nil, errors.New("pkg: cannot read " + n)
			}
			return []byte("name: " + n + "\n"), nil
		case "unregister":
			for _, n := range argv[6:] {
				var keep []string
				for _, l := range r.jl {
					if !strings.HasPrefix(l, n+"-") {
						keep = append(keep, l)
					}
				}
				r.jl = keep
			}
		case "register":
			f := argv[6]
			data, err := r.w.Files.ReadFile(bg, f)
			if err != nil {
				return nil, err
			}
			n := strings.TrimSuffix(f[strings.LastIndex(f, "/")+1:], ".ucl")
			_ = data
			for _, l := range r.gen {
				if strings.HasPrefix(l, n+"-") {
					r.jl = append(r.jl, l)
				}
			}
		}
		return nil, nil
	}
	return r
}

func (r *recordsWorld) did(sub string) int {
	n := 0
	for _, c := range r.calls {
		if strings.Contains(c, " "+sub+" ") {
			n++
		}
	}
	return n
}

func TestBaseRecordsFollowTheGeneration(t *testing.T) {
	r := newRecordsWorld(t,
		[]string{"FreeBSD-runtime-15.1p7", "FreeBSD-ftp-15.1", "FreeBSD-clibs-15.1p7"},
		[]string{"FreeBSD-runtime-15.1p6", "FreeBSD-clibs-15.1p7", "tree-2.1.3"})
	res, err := r.w.S.RepairBasePackages(bg, "alpha_web")
	if err != nil {
		t.Fatal(err)
	}
	if res.Base != 3 || res.Replaced != 1 || res.Added != 1 {
		t.Fatalf("result %+v", res)
	}
	have := strings.Join(r.jl, " ")
	for _, want := range []string{"FreeBSD-runtime-15.1p7", "FreeBSD-ftp-15.1", "FreeBSD-clibs-15.1p7", "tree-2.1.3"} {
		if !strings.Contains(have, want) {
			t.Fatalf("records %q miss %s", have, want)
		}
	}
	if strings.Contains(have, "15.1p6") {
		t.Fatalf("the old record is still there: %q", have)
	}
	if got := r.did("unregister"); got != 1 {
		t.Fatalf("unregister calls %d: %v", got, r.calls)
	}
	// a second pass finds nothing to do
	r.calls = nil
	if res, err := r.w.S.RepairBasePackages(bg, "alpha_web"); err != nil || res.Replaced+res.Added != 0 || r.did("unregister")+r.did("register") != 0 {
		t.Fatalf("second pass %+v %v %v", res, err, r.calls)
	}
}

func TestBaseRecordsLeaveTheJailAloneWhenAManifestCannotBeRead(t *testing.T) {
	r := newRecordsWorld(t, []string{"FreeBSD-runtime-15.1p7", "FreeBSD-ftp-15.1"}, []string{"FreeBSD-runtime-15.1p6"})
	r.failInfo = "FreeBSD-ftp"
	if _, err := r.w.S.RepairBasePackages(bg, "alpha_web"); err == nil {
		t.Fatal("no error")
	}
	if r.did("unregister")+r.did("register") != 0 {
		t.Fatalf("the database was changed before every manifest was read: %v", r.calls)
	}
	if strings.Join(r.jl, " ") != "FreeBSD-runtime-15.1p6" {
		t.Fatalf("records %v", r.jl)
	}
}

func TestStartDoesNotFailWhenTheBaseRecordsCannotBeSynced(t *testing.T) {
	r := newRecordsWorld(t, []string{"FreeBSD-runtime-15.1p7"}, []string{"FreeBSD-runtime-15.1p6"})
	r.failInfo = "FreeBSD-runtime"
	var told []string
	r.w.S.Warn = func(_ context.Context, jail, action, what string) { told = append(told, jail+" "+action+" "+what) }
	if err := r.w.S.StartJail(bg, "alpha_web"); err != nil {
		t.Fatalf("the start failed: %v", err)
	}
	if !r.w.Jails.IsUp("alpha_web") {
		t.Fatal("not running")
	}
	if len(told) != 1 || !strings.Contains(told[0], "base-packages") {
		t.Fatalf("told %v", told)
	}
}

func TestStartBringsTheBaseRecordsInStepAndIsQuietWhenTheyAre(t *testing.T) {
	r := newRecordsWorld(t, []string{"FreeBSD-runtime-15.1p7"}, []string{"FreeBSD-runtime-15.1p6", "tree-2.1.3"})
	if err := r.w.S.StartJail(bg, "alpha_web"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.jl, " "); !strings.Contains(got, "FreeBSD-runtime-15.1p7") || !strings.Contains(got, "tree-2.1.3") || strings.Contains(got, "p6") {
		t.Fatalf("records %q", got)
	}
}

// An upgrade, a rollback and a conversion start the jail through doStart: its records follow the generation there too.
func TestEveryWayOfStartingBringsTheBaseRecordsInStep(t *testing.T) {
	r := newRecordsWorld(t, []string{"FreeBSD-runtime-15.1p7"}, []string{"FreeBSD-runtime-15.1p6", "tree-2.1.3"})
	var told []error
	r.w.S.BaseRecords = func(_ context.Context, jail string, err error) { told = append(told, err) }
	if err := r.w.S.doStart(bg, "alpha_web"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.jl, " "); !strings.Contains(got, "FreeBSD-runtime-15.1p7") || strings.Contains(got, "p6") || !strings.Contains(got, "tree-2.1.3") {
		t.Fatalf("records %q", got)
	}
	if len(told) != 1 || told[0] != nil {
		t.Fatalf("the service was told %v", told)
	}
}
