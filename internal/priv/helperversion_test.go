package priv

import (
	"errors"
	"testing"

	"jailops.net/jailops/sdk/api"
)

func TestHelperOlderThanDaemonIsRefused(t *testing.T) {
	for _, c := range []struct {
		helper, daemon string
		skew           bool
	}{
		{"0.8.6", "0.9.0", true},
		{"v0.9.0", "0.9.1", true},
		{"0.9.0", "0.9.0", false},
		{"0.9.1", "0.9.0", false},
		{"1.0.0", "0.9.9", false},
		{"", "0.9.0", false},
		{"dev", "0.9.0", false},
		{"0.9.0", "dev", false},
	} {
		err := CheckHelperVersion(c.helper, c.daemon)
		var ae *api.Error
		got := errors.As(err, &ae) && ae.Code == api.CodeVersionSkew
		if got != c.skew {
			t.Errorf("helper %q daemon %q: skew = %v, want %v (%v)", c.helper, c.daemon, got, c.skew, err)
		}
	}
}
