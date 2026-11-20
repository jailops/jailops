package api

import (
	"strings"
	"testing"
)

func TestValidNotes(t *testing.T) {
	ok := []string{"", "one line", "# Title\n\n- a\n\t- b\n```\ncode\n```", "Ünïcode ✓", strings.Repeat("x", MaxNotes)}
	for _, s := range ok {
		if err := ValidNotes(s); err != nil {
			t.Errorf("%.40q: %v", s, err)
		}
	}
	bad := map[string]string{
		"too long":        strings.Repeat("x", MaxNotes+1),
		"carriage return": "a\r\nb",
		"escape":          "a\x1b]0;title\x07",
		"NUL":             "a\x00b",
		"DEL":             "a\x7fb",
		"bidi":            "a‮b",
		"zero width":      "a​b",
		"not UTF-8":       "a\xffb",
	}
	for name, s := range bad {
		if err := ValidNotes(s); !IsCode(err, CodeInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := CheckNotes(JailSpec{Notes: "a\x00"}); err == nil {
		t.Error("CheckNotes passed a NUL in a jail spec")
	}
	if err := CheckNotes(WireGuardSpec{}); err != nil {
		t.Error(err)
	}
}
