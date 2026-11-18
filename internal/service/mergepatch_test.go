package service

import (
	"encoding/json"
	"testing"
)

// A merge patch keeps the numbers of the object and of the patch as they are written: 2^53+1 does not become 2^53.
func TestMergePatchKeepsLargeIntegers(t *testing.T) {
	type obj struct {
		Big   int64  `json:"big"`
		Other int64  `json:"other"`
		Name  string `json:"name"`
	}
	cur := obj{Big: 9007199254740993, Name: "a"}
	patch := map[string]any{"name": "b", "other": json.Number("9007199254740995")}
	var out obj
	if err := MergePatch(cur, patch, &out); err != nil {
		t.Fatal(err)
	}
	if out.Big != 9007199254740993 || out.Other != 9007199254740995 || out.Name != "b" {
		t.Fatalf("%+v", out)
	}
}
