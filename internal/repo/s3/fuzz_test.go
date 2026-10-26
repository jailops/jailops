package s3

import (
	"net/url"
	"strings"
	"testing"
)

func FuzzDecodeList(f *testing.F) {
	f.Add(`<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>t</NextContinuationToken><EncodingType>url</EncodingType><Contents><Key>p%2Fa+b</Key><LastModified>2026-01-02T03:04:05.000Z</LastModified><Size>5</Size></Contents></ListBucketResult>`)
	f.Add(`<ListBucketResult></ListBucketResult>`)
	f.Add(`<ListBucketResult><Contents><Key>%zz</Key></Contents><EncodingType>url</EncodingType></ListBucketResult>`)
	f.Add(`not xml`)
	f.Fuzz(func(t *testing.T, doc string) {
		pg, err := decodeList(strings.NewReader(doc), "repo/", "p/")
		if err != nil {
			return
		}
		for _, o := range pg.Objects {
			if !strings.HasPrefix(o.Key, "p/") || o.Size < 0 || o.ModTime.IsZero() {
				t.Fatalf("accepted a bad entry %+v", o)
			}
		}
	})
}

func FuzzContentRange(f *testing.F) {
	f.Add("bytes 0-9/100")
	f.Add("bytes */100")
	f.Add("bytes -5")
	f.Add("")
	f.Fuzz(func(t *testing.T, v string) {
		if n, ok := contentRangeStart(v); ok && n < 0 {
			t.Fatalf("negative start %d from %q", n, v)
		}
	})
}

func FuzzURIEncode(f *testing.F) {
	f.Add("a b/c+d%e")
	f.Add("日本/\x00\xff")
	f.Fuzz(func(t *testing.T, k string) {
		enc := encodePath(k)
		if strings.ContainsAny(enc, " +?#&=\"'<>") {
			t.Fatalf("unescaped character in %q", enc)
		}
		dec, err := url.PathUnescape(enc)
		if err != nil || dec != k {
			t.Fatalf("%q -> %q -> %q (%v)", k, enc, dec, err)
		}
		u := buildURL("https", "h", "/b/"+enc, map[string]string{"prefix": k})
		if u.EscapedPath() != "/b/"+enc {
			t.Fatalf("the transport would send %q, signed %q", u.EscapedPath(), "/b/"+enc)
		}
		q, err := url.ParseQuery(u.RawQuery)
		if err != nil || q.Get("prefix") != k {
			t.Fatalf("query round trip of %q: %q %v", k, q.Get("prefix"), err)
		}
	})
}

func FuzzParseError(f *testing.F) {
	f.Add(`<Error><Code>NoSuchKey</Code><Message>m</Message></Error>`)
	f.Add(`garbage`)
	f.Fuzz(func(t *testing.T, doc string) {
		resp := fakeResponse(404, doc)
		e := parseError("get", "k", resp)
		if e.Status != 404 || e.Error() == "" {
			t.Fatal("bad error")
		}
	})
}
