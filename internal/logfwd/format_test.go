package logfwd

import (
	"encoding/json"
	"io/fs"
	"strings"
	"testing"
	"time"

	"jailops.net/jailops/sdk/api"
)

var t0 = time.Date(2026, 11, 1, 9, 30, 0, 0, time.UTC)

// The sample of docs/logging.md, byte for byte.
func TestFormat5424(t *testing.T) {
	e := Entry{Time: t0, Project: "web", Jail: "nginx", App: "nginx", File: "messages", Line: "Nov  1 09:30:00 nginx sshd[42]: Accepted publickey for deploy"}
	fac, _ := api.SyslogFacility("local3")
	got := string(Format5424(e, fac, "host1.example.com"))
	want := `<158>1 2026-11-01T09:30:00.000000Z host1.example.com jailops - log [jailops project="web" jail="nginx" app="nginx" file="messages"] Nov  1 09:30:00 nginx sshd[42]: Accepted publickey for deploy`
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	// the audit log, a hostile structured-data value and an empty host name
	a := Entry{Time: t0, Project: `we"b]\`, File: api.LogSourceAudit, Line: "x"}
	got = string(Format5424(a, 16, " "))
	want = `<134>1 2026-11-01T09:30:00.000000Z - jailops - audit [jailops project="we\"b\]\\" jail="" app="" file="audit"] x`
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

// A line is data: controls are escaped (no second message, no terminal
// sequence), invalid UTF-8 is replaced and the result is at most 8 KiB.
func TestEscape(t *testing.T) {
	for in, want := range map[string]string{
		"plain ü":                     "plain ü",
		"a\nb":                        `a\nb`,
		"a\r\n<13>1 forged":           `a\r\n<13>1 forged`,
		"t\tab":                       `t\tab`,
		"\x1b[2Jx\x00\x7f":            `\x1b[2Jx\x00\x7f`,
		"c1\u009b":                    `c1\u009b`,
		"bad\xffutf8":                 "bad�utf8",
		strings.Repeat("é", 5000):     strings.Repeat("é", 4096),
		strings.Repeat("\x01", 10000): strings.Repeat(`\x01`, 2048),
	} {
		got := Escape(in)
		if got != want {
			t.Errorf("Escape(%.20q) = %.40q (%d bytes), want %.40q (%d bytes)", in, got, len(got), want, len(want))
		}
		if len(got) > api.MaxLogForwardLine {
			t.Errorf("%d bytes", len(got))
		}
	}
	msg := string(Format5424(Entry{Time: t0, Line: "one\n<14>1 2026-01-01T00:00:00Z evil jailops - log - forged"}, 16, "h"))
	if strings.Count(msg, "\n") != 0 || strings.Count(msg, "<") != 2 {
		t.Fatalf("%s", msg)
	}
}

func TestFormatJSON(t *testing.T) {
	b := FormatJSON(Entry{Time: t0, Project: "web", Jail: "nginx", File: "messages", Line: "a\x1bb"}, "host1")
	if !strings.HasSuffix(string(b), "}\n") {
		t.Fatalf("%q", b)
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["host"] != "host1" || m["jail"] != "nginx" || m["line"] != `a\x1bb` || m["time"] != "2026-11-01T09:30:00Z" || m["app"] != "" {
		t.Fatalf("%v", m)
	}
}

func TestQueueDropsOldest(t *testing.T) {
	q := newQueue(3)
	for i := range 5 {
		d, ok := q.push(Entry{Line: string(rune('a' + i))})
		if ok != (i >= 3) || (ok && d.Line != string(rune('a'+i-3))) {
			t.Fatalf("push %d: %v %q", i, ok, d.Line)
		}
	}
	if q.len() != 3 || q.dropped != 2 {
		t.Fatalf("len %d dropped %d", q.len(), q.dropped)
	}
	got := q.peek(10)
	if len(got) != 3 || got[0].Line != "c" || got[2].Line != "e" {
		t.Fatalf("%v", got)
	}
	q.pop(2)
	if p := q.peek(10); len(p) != 1 || p[0].Line != "e" {
		t.Fatalf("%v", p)
	}
}

func TestBackoff(t *testing.T) {
	for _, c := range []struct {
		n        int
		jitter   float64
		min, max time.Duration
	}{
		{1, 0, 500 * time.Millisecond, 500 * time.Millisecond},
		{1, 0.999, 500 * time.Millisecond, time.Second},
		{2, 0, time.Second, time.Second},
		{4, 0.5, 6 * time.Second, 6 * time.Second},
		{9, 0, 128 * time.Second, 128 * time.Second},
		{10, 0, 150 * time.Second, 150 * time.Second},
		{50, 0.999, 299 * time.Second, 300 * time.Second},
	} {
		d := backoff(c.n, c.jitter)
		if d < c.min || d > c.max {
			t.Errorf("backoff(%d, %v) = %v, want %v..%v", c.n, c.jitter, d, c.min, c.max)
		}
	}
}

func TestCheckMode(t *testing.T) {
	for _, c := range []struct {
		mode   fs.FileMode
		uid    uint32
		secret bool
		ok     bool
	}{
		{0o644, 0, false, true},
		{0o600, 0, true, true},
		{0o640, 0, true, true},
		{0o644, 0, true, false},  // the token readable by everyone
		{0o664, 0, false, false}, // writable by the group
		{0o600, 1001, true, false},
		{fs.ModeSymlink | 0o777, 0, false, false},
		{fs.ModeDir | 0o755, 0, false, false},
	} {
		err := checkMode("/f", c.mode, c.uid, c.secret)
		if (err == nil) != c.ok {
			t.Errorf("%v uid %d secret %v: %v", c.mode, c.uid, c.secret, err)
		}
	}
}
