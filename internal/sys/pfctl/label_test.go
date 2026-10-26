package pfctl

import "testing"

func TestOnlyJailOpsLabelsCanBeKilled(t *testing.T) {
	for _, ok := range []string{"jo-0123456789ab", "jo-ffffffffffff"} {
		if err := ValidateLabel(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "jo-", "jo-0123456789a", "jo-0123456789abc", "JO-0123456789ab", "x-0123456789ab", "jo-0123456789ag", "jo-0123456789ab\n", `jo-0123456789ab" -k`, "all", "*"} {
		if err := ValidateLabel(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	got := LabelsOf("pass in quick on e all label \"jo-0123456789ab\"\nblock drop out on e all\npass quick on x label \"other\"\npass out label \"jo-ffffffffffff\"")
	if len(got) != 2 || !got["jo-0123456789ab"] || !got["jo-ffffffffffff"] {
		t.Fatalf("%v", got)
	}
}
