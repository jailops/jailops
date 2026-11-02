package api

import (
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The forms a unified read orders by, the RFC 5424 head included, and the year rule of the syslog form.
func TestLogLineTimeForms(t *testing.T) {
	jan := time.Date(2027, 1, 1, 0, 5, 0, 0, time.UTC)
	for _, c := range []struct {
		line string
		now  time.Time
		want string
	}{
		{"<13>1 2026-10-29T11:58:00.123456+00:00 web sshd 42 - - x", logNow, "2026-10-29T11:58:00.123456Z"},
		{"<165>1 2026-10-29T11:58:00Z host app - ID47 [ex@32473 a=\"b\"] x", logNow, "2026-10-29T11:58:00Z"},
		{"2026-10-29T11:58:00Z", logNow, "2026-10-29T11:58:00Z"},
		{"2026-10-29T13:58:00+02:00 x", logNow, "2026-10-29T11:58:00Z"},
		{"Oct 29 11:58:00 web x", logNow, "2026-10-29T11:58:00Z"},
		{"Dec 31 23:59:59 web x", jan, "2026-12-31T23:59:59Z"},    // read just after new year: last year
		{"Jan  1 00:04:00 web x", jan, "2027-01-01T00:04:00Z"},    // this year
		{"Nov  2 00:00:00 web x", logNow, "2025-11-02T00:00:00Z"}, // more than a day ahead: last year
	} {
		got, ok := LogLineTime(c.line, c.now)
		if !ok || got.UTC().Format(time.RFC3339Nano) != c.want {
			t.Errorf("%q: %v %v, want %s", c.line, got, ok, c.want)
		}
	}
	for _, line := range []string{"<13>1 - web sshd - - - no time", "<abc>1 2026-10-29T11:58:00Z x", "<13>2 2026-10-29T11:58:00Z x"} {
		if _, ok := LogLineTime(line, logNow); ok {
			t.Errorf("%q parsed", line)
		}
	}
}

func joinEntries(es []LogEntry) string {
	var out []string
	for _, e := range es {
		out = append(out, e.Jail+":"+e.Line)
	}
	return strings.Join(out, " ")
}

// Merge: by time; a line without a timestamp stays after the line before it
// in its file (and leading ones before the first timestamp); equal times keep
// the order of the sources; only the newest lines are kept.
func TestMergeLogs(t *testing.T) {
	src := []LogSourceLines{
		{Jail: "a", Lines: []string{"cont-a0", "Oct 29 11:00:02 a1", "Oct 29 11:00:05 a2", "  trace a2", "Oct 29 11:00:05 a3"}},
		{Jail: "b", Lines: []string{"Oct 29 11:00:01 b1", "Oct 29 11:00:05 b2", "Oct 29 11:00:09 b3"}},
		{Jail: "c", Lines: []string{"no time 1", "no time 2"}},
	}
	got, cut := MergeLogs(src, 100, logNow)
	want := "c:no time 1 c:no time 2 b:Oct 29 11:00:01 b1 a:cont-a0 a:Oct 29 11:00:02 a1 a:Oct 29 11:00:05 a2 a:  trace a2 a:Oct 29 11:00:05 a3 b:Oct 29 11:00:05 b2 b:Oct 29 11:00:09 b3"
	if joinEntries(got) != want || cut {
		t.Fatalf("got\n%s\nwant\n%s", joinEntries(got), want)
	}
	if got[0].Time != nil || got[2].Time == nil || got[3].Time != nil || !got[2].Time.Equal(time.Date(2026, 10, 29, 11, 0, 1, 0, time.UTC)) {
		t.Fatalf("times %+v %+v", got[0], got[2])
	}
	got, cut = MergeLogs(src, 3, logNow)
	if joinEntries(got) != "a:Oct 29 11:00:05 a3 b:Oct 29 11:00:05 b2 b:Oct 29 11:00:09 b3" || !cut {
		t.Fatalf("%s %v", joinEntries(got), cut)
	}
	if got, cut := MergeLogs(nil, 10, logNow); len(got) != 0 || cut {
		t.Fatalf("%v", got)
	}
}

func TestLogsQuery(t *testing.T) {
	q := LogsQuery{Jails: []string{"web", "db"}, Labels: map[string]string{"tier": "front", "a": "b"}, App: "nginx", AppLogs: true,
		File: "auth.log", Tail: 50, Since: "1h", Grep: "x", Cursor: map[string]int64{"p/web/auth.log": 42}}
	v := q.Values()
	if v.Encode() != "app=nginx&app_logs=true&cursor=%7B%22p%2Fweb%2Fauth.log%22%3A42%7D&file=auth.log&grep=x&jail=web&jail=db&label=a%3Db&label=tier%3Dfront&since=1h&tail=50" {
		t.Fatalf("%s", v.Encode())
	}
	back, err := ParseLogsQuery(v)
	if err != nil || !reflect.DeepEqual(back, q) {
		t.Fatalf("%v\n%+v\n%+v", err, back, q)
	}
	d, err := ParseLogsQuery(url.Values{})
	if err != nil || d.File != DefaultLogFile || d.Tail != DefaultLogTail || len(d.Values()) != 0 {
		t.Fatalf("%+v %v", d, err)
	}
	for _, bad := range []string{"tail=0", "tail=5001", "cursor=%5B%5D", "cursor=%7B%22x%22%3A1%7D", "cursor=%7B%22a%2Fb%2Fc%22%3A-1%7D", "label=x",
		"jail=Web", "app=..", "file=..%2Fx", "file=messages.0.gz", "app_logs=yes", "since=later"} {
		v, _ := url.ParseQuery(bad)
		if _, err := ParseLogsQuery(v); !IsCode(err, CodeInvalid) {
			t.Errorf("%s: %v", bad, err)
		}
	}
	many := url.Values{}
	for range MaxLogSources + 1 {
		many.Add("jail", "web")
	}
	if _, err := ParseLogsQuery(many); !IsCode(err, CodeInvalid) {
		t.Errorf("too many jails: %v", err)
	}
}

func TestLogForwardValidate(t *testing.T) {
	ok := []LogSink{
		{Name: "central", Type: LogSinkSyslog, To: "tls://logs.example.com:6514", Facility: "local3", CAFile: "/usr/local/etc/jailops/ca.pem"},
		{Name: "lan", Type: LogSinkSyslog, To: "udp://192.0.2.10"},
		{Name: "v6", Type: LogSinkSyslog, To: "tcp://[2001:db8::10]:1514", Queue: 500,
			Sources: LogSinkSource{Projects: []string{"web"}, Labels: map[string]string{"tier": "front"}, Files: []string{"messages", "auth.log"}, Audit: true}},
		{Name: "hook", Type: LogSinkWebhook, To: "https://hooks.example.com/x?k=1", TokenFile: "/usr/local/etc/jailops/hook.token"},
		{Name: "audit-only", Type: LogSinkWebhook, To: "https://hooks.example.com", Sources: LogSinkSource{NoJails: true, Audit: true}},
	}
	if err := (LogForwardSpec{Sinks: ok}).Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []LogSink{
		{Name: "Bad", Type: LogSinkSyslog, To: "udp://h"},
		{Name: "x", Type: "gelf", To: "udp://h"},
		{Name: "x", Type: LogSinkSyslog, To: "https://h"},
		{Name: "x", Type: LogSinkSyslog, To: "udp://h/path"},
		{Name: "x", Type: LogSinkSyslog, To: "udp://user@h"},
		{Name: "x", Type: LogSinkSyslog, To: "udp://h:99999"},
		{Name: "x", Type: LogSinkSyslog, To: "logs.example.com:514"},
		{Name: "x", Type: LogSinkSyslog, To: "udp://h", Facility: "kern"},
		{Name: "x", Type: LogSinkSyslog, To: "udp://h", CAFile: "/ca.pem"},
		{Name: "x", Type: LogSinkSyslog, To: "tls://h", TokenFile: "/t"},
		{Name: "x", Type: LogSinkSyslog, To: "tls://h", CAFile: "relative.pem"},
		{Name: "x", Type: LogSinkWebhook, To: "http://h/x"},
		{Name: "x", Type: LogSinkWebhook, To: "https://h", Facility: "local1"},
		{Name: "x", Type: LogSinkWebhook, To: "https://h", TokenFile: "/a/../b"},
		{Name: "x", Type: LogSinkSyslog, To: "udp://h", Queue: 10},
		{Name: "x", Type: LogSinkSyslog, To: "udp://h", Sources: LogSinkSource{Files: []string{"messages.0"}}},
		{Name: "x", Type: LogSinkSyslog, To: "udp://h", Sources: LogSinkSource{Files: []string{"../etc/passwd"}}},
		{Name: "x", Type: LogSinkSyslog, To: "udp://h", Sources: LogSinkSource{Jails: []string{"A"}}},
		{Name: "x", Type: LogSinkSyslog, To: "udp://h", Sources: LogSinkSource{NoJails: true}},
	} {
		if err := (LogForwardSpec{Sinks: []LogSink{bad}}).Validate(); !IsCode(err, CodeInvalid) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	if err := (LogForwardSpec{Sinks: []LogSink{ok[0], ok[0]}}).Validate(); !IsCode(err, CodeInvalid) {
		t.Errorf("duplicate: %v", err)
	}
}
