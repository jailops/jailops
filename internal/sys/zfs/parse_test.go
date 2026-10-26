package zfs

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseList(t *testing.T) {
	out := "tank/jailops\tfilesystem\ton\t-\ntank/jailops/jails/a\tfilesystem\ton\tjail\n\n"
	got, err := ParseList(out, []string{"jailops:managed", "jailops:kind"})
	if err != nil || len(got) != 2 {
		t.Fatal(got, err)
	}
	if _, ok := got[0].Props["jailops:kind"]; ok {
		t.Error("'-' must be absent")
	}
	if got[1].Props["jailops:kind"] != "jail" || got[1].Type != "filesystem" {
		t.Errorf("%+v", got[1])
	}
	if _, err := ParseList("a\tfilesystem\n", []string{"x"}); err == nil {
		t.Error("short row accepted")
	}
}

func TestParseGet(t *testing.T) {
	m, err := ParseGet("guid\t123\t-\ncompression\tlz4\tinherited from tank\njailops:id\tx y\tlocal\n")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Prop{"guid": {"123", "-"}, "compression": {"lz4", "inherited from tank"}, "jailops:id": {"x y", "local"}}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("%v", m)
	}
	if _, err := ParseGet("a\tb\n"); err == nil {
		t.Error("bad row accepted")
	}
}

func TestParseHoldsClonesPools(t *testing.T) {
	h, err := ParseHolds("tank/a@s\tkeep\t1700000000\n")
	if err != nil || len(h) != 1 || h[0].Tag != "keep" || !h[0].Created.Equal(time.Unix(1700000000, 0)) {
		t.Fatal(h, err)
	}
	if got := ParseClones("tank/c1,tank/c2"); !reflect.DeepEqual(got, []string{"tank/c1", "tank/c2"}) {
		t.Error(got)
	}
	if ParseClones("-") != nil || ParseClones("") != nil {
		t.Error("none")
	}
	ps, err := ParsePools("tank\t1000\t400\t600\tONLINE\nbad\t100\t-\t-\tFAULTED\n")
	if err != nil || len(ps) != 2 || ps[0].Free != 600 || ps[1].Health != "FAULTED" {
		t.Fatal(ps, err)
	}
	if _, err := ParseUint("12x"); err == nil {
		t.Error("ParseUint")
	}
}

func TestParseActiveFeatures(t *testing.T) {
	out := "size\t100\nfeature@async_destroy\tactive\nfeature@encryption\tenabled\nfeature@zstd_compress\tactive\nfeature@x\tdisabled\n"
	got, err := ParseActiveFeatures(out)
	if err != nil || !reflect.DeepEqual(got, []string{"async_destroy", "zstd_compress"}) {
		t.Fatal(got, err)
	}
}

func FuzzParseList(f *testing.F) {
	f.Add("a\tfilesystem\t-\n", 1)
	f.Add("\t\t\n", 0)
	f.Fuzz(func(t *testing.T, s string, n int) {
		if n < 0 || n > 8 {
			n = 1
		}
		ParseList(s, make([]string, n))
	})
}

func FuzzParseGet(f *testing.F) {
	f.Add("a\tb\tc\n")
	f.Fuzz(func(t *testing.T, s string) { ParseGet(s) })
}

func FuzzParseOthers(f *testing.F) {
	f.Add("tank\t1\t2\t3\tONLINE\n")
	f.Add("feature@a\tactive\n")
	f.Fuzz(func(t *testing.T, s string) {
		ParseHolds(s)
		ParsePools(s)
		ParseActiveFeatures(s)
		ParseClones(s)
		ParseUint(s)
		ParseCompat(s)
	})
}

func FuzzParseCompat(f *testing.F) {
	f.Add("# c\nasync_destroy, bookmarks\nlz4_compress\n", "async_destroy")
	f.Fuzz(func(t *testing.T, content, feat string) {
		r := CheckCompatDetail([]string{feat}, content)
		if strings.TrimSpace(content) == "" && r.Verdict != Unknown {
			t.Fatal("empty content must be UNKNOWN")
		}
	})
}
