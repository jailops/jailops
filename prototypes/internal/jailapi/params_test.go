package jailapi

import "testing"

func TestParams(t *testing.T) {
	p := Str("name", "jop1x")
	if string(p.Val) != "jop1x\x00" {
		t.Fatal(p.Val)
	}
	v, err := AsInt32(Int32("jid", 42).Val)
	if err != nil || v != 42 {
		t.Fatal(v, err)
	}
	if len(Flag("persist").Val) != 0 {
		t.Fatal("flag has value")
	}
	if CString([]byte("ab\x00cd")) != "ab" || CString([]byte("ab")) != "ab" {
		t.Fatal("cstring")
	}
	if _, err := AsInt32([]byte{1}); err == nil {
		t.Fatal("short")
	}
}

func TestParseJLS(t *testing.T) {
	js := ParseJLSn("jid=3 name=jop1a path=/x\njid=4 name=other path=/\n\n")
	if len(js) != 2 || js[0]["name"] != "jop1a" || js[1]["jid"] != "4" {
		t.Fatal(js)
	}
	if len(ParseJLSn("")) != 0 {
		t.Fatal("empty")
	}
}

func TestOwned(t *testing.T) {
	if !OwnedName("jop1a") || OwnedName("jop1") || OwnedName("web") || OwnedName("jop2a") {
		t.Fatal()
	}
}
