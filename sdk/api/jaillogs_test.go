package api

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

var logNow = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

func TestLogLineTime(t *testing.T) {
	for line, want := range map[string]string{
		"2026-10-29T11:58:00Z sshd[1]: x":                 "2026-10-29T11:58:00Z",
		"2026-10-29T11:58:00.123+02:00 x":                 "2026-10-29T09:58:00.123Z",
		"2026-10-29 11:58:00,500 INFO x":                  "2026-10-29T11:58:00Z",
		"Oct 29 11:58:00 web kernel: x":                   "2026-10-29T11:58:00Z",
		"Oct  5 03:04:05 web cron[2]: x":                  "2026-10-05T03:04:05Z",
		"Dec 31 23:00:00 web x":                           "2025-12-31T23:00:00Z", // in the future: the year before
		`10.0.0.1 - - [29/Oct/2026:11:58:00 +0100] "GET"`: "2026-10-29T10:58:00Z",
		`10.0.0.1 - - [29/Oct/2026:11:58:00] "GET"`:       "2026-10-29T11:58:00Z",
	} {
		got, ok := LogLineTime(line, logNow)
		if !ok || got.UTC().Format(time.RFC3339Nano) != want {
			t.Errorf("%q: %v %v, want %s", line, got, ok, want)
		}
	}
	for _, line := range []string{"", "   continued", "no timestamp here", "Foo 29 11:58:00 x", "[not a time] x", "2026-13-01 00:00:00"} {
		if _, ok := LogLineTime(line, logNow); ok {
			t.Errorf("%q parsed", line)
		}
	}
}

func TestParseLogSince(t *testing.T) {
	for s, want := range map[string]time.Time{
		"":                     {},
		"15m":                  logNow.Add(-15 * time.Minute),
		"2h30m":                logNow.Add(-150 * time.Minute),
		"1d":                   logNow.Add(-24 * time.Hour),
		"2026-10-29T11:00:00Z": logNow.Add(-time.Hour),
	} {
		got, err := ParseLogSince(s, logNow)
		if err != nil || !got.Equal(want) {
			t.Errorf("%q: %v %v", s, got, err)
		}
	}
	for _, s := range []string{"yesterday", "-5m", "5", "2026-10-29"} {
		if _, err := ParseLogSince(s, logNow); !IsCode(err, CodeInvalid) {
			t.Errorf("%q: %v", s, err)
		}
	}
}

func TestLogQuery(t *testing.T) {
	q, err := ParseLogQuery(url.Values{})
	if err != nil || q.Tail != DefaultLogTail {
		t.Fatalf("%+v %v", q, err)
	}
	q, err = ParseLogQuery(url.Values{"tail": {"50"}, "since": {"15m"}, "grep": {"Error"}, "offset": {"1200"}})
	if err != nil || q != (JailLogQuery{Tail: 50, Since: "15m", Grep: "Error", Offset: 1200}) {
		t.Fatalf("%+v %v", q, err)
	}
	if got := q.Values().Encode(); got != "grep=Error&offset=1200&since=15m&tail=50" {
		t.Errorf("values %q", got)
	}
	if got := (JailLogQuery{Tail: DefaultLogTail}).Values().Encode(); got != "" {
		t.Errorf("defaults are left out: %q", got)
	}
	for _, v := range []url.Values{
		{"tail": {"0"}}, {"tail": {"x"}}, {"tail": {"5001"}}, {"tail": {"-1"}},
		{"offset": {"-1"}}, {"offset": {"x"}}, {"since": {"soon"}}, {"grep": {strings.Repeat("a", 129)}},
	} {
		if _, err := ParseLogQuery(v); !IsCode(err, CodeInvalid) {
			t.Errorf("%v: %v", v, err)
		}
	}
}

func TestLogFileNamesAndKinds(t *testing.T) {
	for _, n := range []string{"messages", "messages.0", "messages.1.gz", "nginx/access.log", "a_b-c.d", "0"} {
		if err := ValidLogFileName(n); err != nil {
			t.Errorf("%q: %v", n, err)
		}
	}
	for _, n := range []string{"", ".", "..", ".hidden", "a/.b", "a/b/c", "/etc/passwd", "a//b", "a b", "a\x00", "a/", "../a", strings.Repeat("a", 513)} {
		if err := ValidLogFileName(n); err == nil {
			t.Errorf("%q accepted", n)
		}
	}
	for n, want := range map[string]struct {
		kind       JailLogKind
		compressed bool
	}{
		"messages": {JailLogActive, false}, "messages.0": {JailLogRotated, false}, "messages.1.gz": {JailLogRotated, true},
		"auth.log.2.bz2": {JailLogRotated, true}, "x.3.xz": {JailLogRotated, true}, "nginx/access.log": {JailLogActive, false},
		"nginx/access.log.1": {JailLogRotated, false}, "php7.4": {JailLogRotated, false},
	} {
		k, c := LogFileKind(n)
		if k != want.kind || c != want.compressed {
			t.Errorf("%s: %s %v", n, k, c)
		}
	}
}

const sampleLog = "Oct 29 11:00:00 web sshd[1]: Accepted publickey\n" +
	"Oct 29 11:30:00 web cron[2]: (root) CMD (job)\n" +
	"  continued from cron\n" +
	"Oct 29 11:50:00 web nginx[3]: Error: upstream timed out\n" +
	"Oct 29 11:55:00 web sshd[1]: Disconnected\n"

func TestAssembleLogRead(t *testing.T) {
	data := []byte(sampleLog)
	size := int64(len(data))
	q := JailLogQuery{Tail: DefaultLogTail}
	r := AssembleLogRead("web", "messages", data, 0, size, q, logNow)
	if r.Jail != "web" || r.File != "messages" || len(r.Lines) != 5 || r.NextOffset != size || r.Rotated || r.Truncated {
		t.Fatalf("%+v", r)
	}
	if r.Lines[0].N != 1 || r.Lines[4].N != 5 || r.Lines[2].Text != "  continued from cron" {
		t.Fatalf("%+v", r.Lines)
	}
	// since keeps a continuation line with its predecessor
	r = AssembleLogRead("web", "messages", data, 0, size, JailLogQuery{Tail: 200, Since: "45m"}, logNow)
	if got := texts(r); got != "Oct 29 11:30:00 web cron[2]: (root) CMD (job)|  continued from cron|Oct 29 11:50:00 web nginx[3]: Error: upstream timed out|Oct 29 11:55:00 web sshd[1]: Disconnected" {
		t.Errorf("since: %q", got)
	}
	// grep is a case-insensitive substring
	r = AssembleLogRead("web", "messages", data, 0, size, JailLogQuery{Tail: 200, Grep: "error"}, logNow)
	if got := texts(r); got != "Oct 29 11:50:00 web nginx[3]: Error: upstream timed out" || r.Lines[0].N != 4 {
		t.Errorf("grep: %q %+v", got, r.Lines)
	}
	// tail cuts from the front and says so
	r = AssembleLogRead("web", "messages", data, 0, size, JailLogQuery{Tail: 2}, logNow)
	if got := texts(r); got != "Oct 29 11:50:00 web nginx[3]: Error: upstream timed out|Oct 29 11:55:00 web sshd[1]: Disconnected" || !r.Truncated {
		t.Errorf("tail: %q %v", got, r.Truncated)
	}
	// a continuation from an offset: no line numbers, nothing dropped
	off := int64(strings.Index(sampleLog, "Oct 29 11:55"))
	r = AssembleLogRead("web", "messages", data[off:], off, size, JailLogQuery{Tail: 200, Offset: off}, logNow)
	if len(r.Lines) != 1 || r.Lines[0].N != 0 || r.Truncated || r.Rotated || r.NextOffset != size {
		t.Errorf("offset: %+v", r)
	}
	// a window that began after the offset: the partial first line is dropped
	r = AssembleLogRead("web", "messages", data[off+3:], off+3, size, JailLogQuery{Tail: 200, Offset: off}, logNow)
	if len(r.Lines) != 0 || !r.Truncated {
		t.Errorf("partial: %+v", r)
	}
	// the file shrank below the offset: rotated, read from the start
	r = AssembleLogRead("web", "messages", data, 0, size, JailLogQuery{Tail: 200, Offset: size + 100}, logNow)
	if !r.Rotated || len(r.Lines) != 5 || r.Lines[0].N != 1 {
		t.Errorf("rotated: %+v", r)
	}
	// an empty file, and a file without a final newline
	r = AssembleLogRead("web", "messages", nil, 0, 0, q, logNow)
	if r.Lines == nil || len(r.Lines) != 0 {
		t.Errorf("empty: %+v", r)
	}
	r = AssembleLogRead("web", "messages", []byte("a\nb"), 0, 3, q, logNow)
	if texts(r) != "a|b" {
		t.Errorf("no final newline: %+v", r)
	}
	if start := LogWindowStart(5000, 0, 1000); start != 4000 {
		t.Errorf("window %d", start)
	}
	if start := LogWindowStart(5000, 4500, 1000); start != 4500 {
		t.Errorf("window %d", start)
	}
	if start := LogWindowStart(500, 4500, 1000); start != 0 {
		t.Errorf("window after rotation %d", start)
	}
}

func texts(r JailLogRead) string {
	var out []string
	for _, l := range r.Lines {
		out = append(out, l.Text)
	}
	return strings.Join(out, "|")
}
