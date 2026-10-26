package zfs

import "testing"

func TestValidateNames(t *testing.T) {
	tests := []struct {
		name string
		fn   func(string) error
		in   string
		ok   bool
	}{
		{"ds", ValidateDataset, "tank", true},
		{"ds", ValidateDataset, "tank/jailops/jails/web-1.v2:x_y", true},
		{"ds", ValidateDataset, "", false},
		{"ds", ValidateDataset, "-tank", false},
		{"ds", ValidateDataset, "tank/-x", false},
		{"ds", ValidateDataset, "tank//x", false},
		{"ds", ValidateDataset, "tank/x/", false},
		{"ds", ValidateDataset, "tank/..", false},
		{"ds", ValidateDataset, "tank/a b", false},
		{"ds", ValidateDataset, "tank/a;rm", false},
		{"ds", ValidateDataset, "1tank/a", false},
		{"ds", ValidateDataset, "tank/a@s", false},
		{"snap", ValidateSnapshot, "tank/a@s1", true},
		{"snap", ValidateSnapshot, "tank/a", false},
		{"snap", ValidateSnapshot, "tank/a@", false},
		{"snap", ValidateSnapshot, "tank/a@b@c", false},
		{"snap", ValidateSnapshot, "-tank/a@b", false},
		{"snap", ValidateSnapshot, "tank/a@-b", false},
		{"bm", ValidateBookmark, "tank/a#m", true},
		{"bm", ValidateBookmark, "tank/a@m", false},
		{"prop", ValidateProperty, "jailops:managed", true},
		{"prop", ValidateProperty, "compression", true},
		{"prop", ValidateProperty, "written@snap1", true},
		{"prop", ValidateProperty, "-o", false},
		{"prop", ValidateProperty, "A:b", false},
		{"prop", ValidateProperty, "a b", false},
		{"prop", ValidateProperty, "foo@bar", false},
		{"val", ValidatePropValue, "on", true},
		{"val", ValidatePropValue, "-x y", true},
		{"val", ValidatePropValue, "a\nb", false},
		{"val", ValidatePropValue, "a\x00b", false},
		{"tag", ValidateTag, "jailops-keep", true},
		{"tag", ValidateTag, "a b", false},
		{"tok", ValidateResumeToken, "1-abc_def-0", true},
		{"tok", ValidateResumeToken, "-1", false},
		{"pool", ValidatePool, "tank", true},
		{"pool", ValidatePool, "tank/a", false},
	}
	for _, tc := range tests {
		if err := tc.fn(tc.in); (err == nil) != tc.ok {
			t.Errorf("%s(%q): err=%v want ok=%v", tc.name, tc.in, err, tc.ok)
		}
	}
}

func TestValidateAny(t *testing.T) {
	for in, want := range map[string]Kind{"p/a": KindDataset, "p/a@s": KindSnapshot, "p/a#b": KindBookmark} {
		if k, err := ValidateAny(in); err != nil || k != want {
			t.Errorf("%q: %v %v", in, k, err)
		}
	}
	if _, err := ValidateAny("-p/a@s"); err == nil {
		t.Error("leading dash accepted")
	}
}

func FuzzValidateAny(f *testing.F) {
	for _, s := range []string{"tank/a@b", "tank#x", "-x", "a//b", "tank/a b"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if _, err := ValidateAny(s); err == nil {
			if s == "" || s[0] == '-' || len(s) > maxNameLen {
				t.Fatalf("accepted %q", s)
			}
			for i := 0; i < len(s); i++ {
				if c := s[i]; c <= ' ' || c == 0x7f || c == '\\' || c == ';' || c == '$' || c == '`' {
					t.Fatalf("accepted %q", s)
				}
			}
		}
	})
}
