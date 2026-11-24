package render

import (
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

// The renderer writes the securelevel it is given, none when it is given none, and refuses one outside -1 to 3. The
// default of a FreeBSD jail is decided by api.EffectiveSecureLevel in the service.
func TestJailConfSecureLevel(t *testing.T) {
	for _, tc := range []struct {
		os   api.OS
		set  *int
		want string
	}{
		{api.OSFreeBSD, nil, "\tsecurelevel = 2;\n"},
		{api.OSFreeBSD, intp(1), "\tsecurelevel = 1;\n"},
		{api.OSFreeBSD, intp(0), "\tsecurelevel = 0;\n"},
		{api.OSFreeBSD, intp(3), "\tsecurelevel = 3;\n"},
		{api.OSFreeBSD, intp(-1), ""},
		{api.OSLinux, nil, ""},
		{api.OSLinux, intp(2), "\tsecurelevel = 2;\n"},
	} {
		in := baseConf()
		in.OS, in.SecureLevel = tc.os, nil
		if in.OS == api.OSLinux {
			in.Allow = nil
		}
		if lvl, ok := api.EffectiveSecureLevel(tc.os, tc.set); ok {
			in.SecureLevel = &lvl
		}
		s, err := JailConf(in)
		if err != nil {
			t.Fatal(err)
		}
		if tc.want == "" && strings.Contains(s, "securelevel") || tc.want != "" && !strings.Contains(s, tc.want) {
			t.Errorf("%s %v: want %q:\n%s", tc.os, tc.set, tc.want, s)
		}
	}
	for _, bad := range []int{-2, 4} {
		in := baseConf()
		in.SecureLevel = intp(bad)
		if _, err := JailConf(in); err == nil {
			t.Errorf("securelevel %d was rendered", bad)
		}
	}
}
