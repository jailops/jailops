package node

import (
	"strings"
	"testing"

	"jailops.net/jailops/internal/repo/format"
)

func FuzzParseRange(f *testing.F) {
	for _, s := range []string{"", "bytes=0-9", "bytes=5-", "bytes=-5", "bytes=9-2", "bytes=0-1,3-4", "items=1-2", "bytes=99999999999999999999-", "bytes=1-x"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, h string) {
		off, length, ok, err := parseRange(h)
		if err != nil {
			return
		}
		if off < 0 || ok && length == 0 || !ok && (off != 0 || length != -1) {
			t.Fatalf("%q -> off %d length %d ok %v", h, off, length, ok)
		}
	})
}

func FuzzChunkIDFromKey(f *testing.F) {
	id := format.ID{0xab, 0xcd}
	f.Add(chunkKey(id))
	f.Add("chunks/ab/cd/" + strings.Repeat("x", 64))
	f.Add("chunks/a/b/c")
	f.Fuzz(func(t *testing.T, key string) {
		got, ok := chunkIDFromKey(key)
		if ok && chunkKey(got) != key {
			t.Fatalf("%q parsed as %s but that is stored at %q", key, got, chunkKey(got))
		}
	})
}

func FuzzSplitSnapKey(f *testing.F) {
	f.Add("groups/" + strings.Repeat("a", 32) + "/20260101000000-0000abcd/commit.json")
	f.Add("groups/x/y/z")
	f.Add("groups//a/b")
	f.Fuzz(func(t *testing.T, key string) {
		g, s, file, ok := splitSnapKey(key)
		if ok && (!format.ValidGroupID(g) || !format.ValidSnapshotID(s) || snapPrefix(g, s)+file != key) {
			t.Fatalf("%q -> %q %q %q", key, g, s, file)
		}
	})
}
