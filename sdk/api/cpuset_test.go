package api

import "testing"

func TestCPUSet(t *testing.T) {
	for in, want := range map[string]string{
		"2,3": "2-3", "0-3,6": "0-3,6", "3,0-1": "0-1,3", "5": "5", "1,1,2": "1-2", "0-0": "0", "07": "7",
	} {
		got, err := NormalizeCPUSet(in)
		if err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", ",", "a", "1,", ",1", "3-1", "1-", "-1", "1 2", "1;2", "1024", "99999", "1--2", "0x1", "-"} {
		if got, err := NormalizeCPUSet(bad); err == nil {
			t.Errorf("%q was accepted as %q", bad, got)
		}
	}
}
