// Package logfwd forwards log lines to syslog servers and webhooks. It reads
// the log files of jails through a read-only reader (the privileged helper's
// fs.tail_file in production) and the daemon's audit log, keeps a bounded
// queue per sink, sends with backoff and records per-source offsets so a
// restart continues where the last delivered line ended (at least once).
// It knows nothing about HTTP or the jail registry: the service hands it
// the jails and the stored offsets.
package logfwd

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"jailops.net/jailops/sdk/api"
)

// Entry is one line to forward.
type Entry struct {
	// Time is the line's own timestamp, or when it was read.
	Time    time.Time
	Project string
	Jail    string
	App     string
	File    string
	Line    string
	// src, begin and end say where the entry came from: the source key and
	// the offsets (or audit ids) before and after the line.
	src        string
	begin, end int64
}

// AppName is the APP-NAME of every message.
const AppName = "jailops"

// severity of every message: informational. A log line carries no
// severity JailOps could trust.
const severity = 6

// Escape makes a log line safe to put in a message: invalid UTF-8 becomes
// U+FFFD and control characters become \t, \n, \r or \xHH (\u00HH for C1),
// so a jail cannot forge a second message or drive a terminal. The result
// is cut at api.MaxLogForwardLine bytes on a character boundary.
func Escape(s string) string {
	var b strings.Builder
	for _, r := range strings.ToValidUTF8(s, "�") {
		if b.Len() >= api.MaxLogForwardLine {
			break
		}
		switch {
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r < 0x20 || r == 0x7f:
			b.WriteString(`\x`)
			b.WriteString(hex2(byte(r)))
		case r >= 0x80 && r <= 0x9f:
			b.WriteString(`\u00`)
			b.WriteString(hex2(byte(r)))
		default:
			b.WriteRune(r)
		}
	}
	out := b.String()
	for len(out) > api.MaxLogForwardLine {
		_, n := utf8.DecodeLastRuneInString(out)
		out = out[:len(out)-n]
	}
	return out
}

func hex2(c byte) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[c>>4], digits[c&15]})
}

// header field: printable US-ASCII without space, at most n bytes, "-" when empty.
func headerField(s string, n int) string {
	var b strings.Builder
	for i := 0; i < len(s) && b.Len() < n; i++ {
		if c := s[i]; c > 32 && c < 127 {
			b.WriteByte(c)
		}
	}
	if b.Len() == 0 {
		return "-"
	}
	return b.String()
}

// sdValue escapes a structured data parameter value (RFC 5424 6.3.3): '"', '\' and ']' take a backslash.
func sdValue(s string) string {
	s = strings.ToValidUTF8(s, "�")
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `]`, `\]`)
	return r.Replace(s)
}

// Format5424 renders e as an RFC 5424 message (without transport framing):
//
//	<134>1 2026-11-01T09:30:00.000000Z host1 jailops - log [jailops project="web" jail="nginx" app="" file="messages"] Nov  1 09:30:00 nginx sshd[42]: ...
//
// The MSG is the escaped line; MSGID is "audit" for the audit log and "log" otherwise.
func Format5424(e Entry, facility int, hostname string) []byte {
	pri := facility*8 + severity
	msgid := "log"
	if e.File == api.LogSourceAudit && e.Jail == "" {
		msgid = "audit"
	}
	var b strings.Builder
	b.Grow(len(e.Line) + 160)
	b.WriteByte('<')
	b.WriteString(strconv.Itoa(pri))
	b.WriteString(">1 ")
	b.WriteString(e.Time.UTC().Format("2006-01-02T15:04:05.000000Z07:00"))
	b.WriteByte(' ')
	b.WriteString(headerField(hostname, 255))
	b.WriteString(" " + AppName + " - " + msgid + " ")
	b.WriteString(`[jailops project="` + sdValue(e.Project) + `" jail="` + sdValue(e.Jail) + `" app="` + sdValue(e.App) + `" file="` + sdValue(e.File) + `"] `)
	b.WriteString(Escape(e.Line))
	return []byte(b.String())
}

// jsonLine is one line of a webhook batch.
type jsonLine struct {
	Time    time.Time `json:"time"`
	Host    string    `json:"host"`
	Project string    `json:"project,omitempty"`
	Jail    string    `json:"jail,omitempty"`
	App     string    `json:"app,omitempty"`
	File    string    `json:"file"`
	Line    string    `json:"line"`
}

// FormatJSON renders e as one line of a webhook batch (JSON, newline-terminated).
func FormatJSON(e Entry, hostname string) []byte {
	b, _ := json.Marshal(jsonLine{Time: e.Time.UTC(), Host: hostname, Project: e.Project, Jail: e.Jail, App: e.App, File: e.File,
		Line: Escape(e.Line)})
	return append(b, '\n')
}
