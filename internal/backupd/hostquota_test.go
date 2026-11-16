package backupd

import "testing"

func TestHostQuotaSizes(t *testing.T) {
	for in, want := range map[string]int64{"": 0, "500G": 500 << 30, "2T": 2 << 40, "100MiB": 100 << 20, "4096": 4096} {
		got, err := (&Config{HostQuota: in}).hostQuota()
		if err != nil || got != want {
			t.Errorf("%q: %d %v", in, got, err)
		}
	}
	for _, in := range []string{"x", "-1G", "0", "G"} {
		if _, err := (&Config{HostQuota: in}).hostQuota(); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}
