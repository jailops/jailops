package validate

import (
	"testing"

	"jailops.net/jailops/sdk/api"
)

func TestSecureLevelRange(t *testing.T) {
	for _, n := range []int{-1, 0, 1, 2, 3} {
		if err := JailSpec("web", api.JailSpec{Image: "fb", SecureLevel: &n}); err != nil {
			t.Errorf("securelevel %d: %v", n, err)
		}
	}
	for _, n := range []int{-2, 4} {
		if err := JailSpec("web", api.JailSpec{Image: "fb", SecureLevel: &n}); err == nil {
			t.Errorf("securelevel %d is accepted", n)
		}
	}
}
