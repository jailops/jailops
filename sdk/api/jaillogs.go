package api

import (
	"bytes"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Jail logs (extension jail_logs): the files below /var/log of a jail's root,
// read through the privileged helper, running or not (a stopped jail's root
// must be mounted). GET /api/v1/jails/{name}/logs lists them;
// GET /api/v1/jails/{name}/logs/{file} reads the end of one.

// JailLogKind says whether a log file is the one being written or a rotated copy.
type JailLogKind string

// Log file kinds.
const (
	JailLogActive  JailLogKind = "active"
	JailLogRotated JailLogKind = "rotated" // messages.0, messages.1.gz, ...
)

// JailLogFile is one log file of a jail.
type JailLogFile struct {
	// Name is the path below /var/log of the jail, at most one directory deep
	// ("messages", "nginx/access.log").
	Name       string      `json:"name"`
	Size       int64       `json:"size"`
	ModifiedAt time.Time   `json:"modified_at"`
	Kind       JailLogKind `json:"kind"`
	// Compressed marks a rotated copy that is compressed (.gz, .bz2, .xz): it
	// is listed but cannot be read through the API.
	Compressed bool `json:"compressed,omitempty"`
}

// JailLogQuery is the query string of GET /api/v1/jails/{name}/logs/{file}.
type JailLogQuery struct {
	// Tail is the number of lines from the end (default DefaultLogTail, at
	// most MaxLogTail).
	Tail int `json:"tail,omitempty"`
	// Since keeps the lines whose timestamp is not older: an RFC 3339 time or
	// a duration before now ("15m", "2h", "1d"). A line without a timestamp
	// of its own belongs to the preceding line's.
	Since string `json:"since,omitempty"`
	// Grep keeps the lines containing this text (plain substring, case
	// insensitive, at most MaxLogGrep bytes).
	Grep string `json:"grep,omitempty"`
	// Offset is where to continue from: the next_offset of a previous reply.
	Offset int64 `json:"offset,omitempty"`
}

// JailLogLine is one line of a log file.
type JailLogLine struct {
	// N is the line number in the file, present when the reply starts at
	// the beginning of the file.
	N    int64  `json:"n,omitempty"`
	Text string `json:"text"`
}

// JailLogRead is the reply of GET /api/v1/jails/{name}/logs/{file}.
type JailLogRead struct {
	Jail  string        `json:"jail"`
	File  string        `json:"file"`
	Lines []JailLogLine `json:"lines"`
	// NextOffset is the size of the file when it was read; send it as offset
	// to get what was written afterwards.
	NextOffset int64 `json:"next_offset"`
	// Rotated is true when the file was shorter than the offset asked for:
	// it was rotated or truncated, and the reply starts at its beginning.
	Rotated bool `json:"rotated,omitempty"`
	// Truncated is true when a limit (tail, the window of MaxLogReadBytes)
	// left lines out.
	Truncated bool `json:"truncated,omitempty"`
}

// HeaderTruncated is set to "true" on the reply of GET
// /api/v1/jails/{name}/logs when the helper's limits (4096 files, 64
// subdirectories) left files out of the listing.
const HeaderTruncated = "X-Jailops-Truncated"

// Limits of a log read.
const (
	DefaultLogTail  = 200
	MaxLogTail      = 5000
	MaxLogGrep      = 128
	MaxLogReadBytes = 1 << 20 // read per request, from the end of the file
	MaxLogFileName  = 512
)

var logSegmentRE = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]*$`)

// ValidLogFileName checks a name relative to /var/log: one or two segments
// of letters, digits, dot, underscore and dash, none starting with a dot.
func ValidLogFileName(name string) error {
	if name == "" || len(name) > MaxLogFileName {
		return Errorf(CodeInvalid, "a log file name is required (at most %d bytes)", MaxLogFileName)
	}
	segs := strings.Split(name, "/")
	if len(segs) > 2 {
		return Errorf(CodeInvalid, "log file %q: at most one directory below /var/log", name)
	}
	for _, s := range segs {
		if len(s) > 255 || !logSegmentRE.MatchString(s) {
			return Errorf(CodeInvalid, "log file %q: a name is letters, digits, dot, underscore and dash, and does not start with a dot", name)
		}
	}
	return nil
}

var rotatedRE = regexp.MustCompile(`\.[0-9]+$`)

// LogFileKind classifies a log file by its name: rotated copies end in a
// number (messages.0) or a compression suffix (messages.1.gz).
func LogFileKind(name string) (kind JailLogKind, compressed bool) {
	base := name
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	for _, ext := range []string{".gz", ".bz2", ".xz"} {
		if strings.HasSuffix(base, ext) {
			return JailLogRotated, true
		}
	}
	if rotatedRE.MatchString(base) {
		return JailLogRotated, false
	}
	return JailLogActive, false
}

// Normalize fills the default tail and checks the limits.
func (q JailLogQuery) Normalize() (JailLogQuery, error) {
	if q.Tail == 0 {
		q.Tail = DefaultLogTail
	}
	if q.Tail < 1 || q.Tail > MaxLogTail {
		return q, Errorf(CodeInvalid, "tail must be between 1 and %d lines", MaxLogTail)
	}
	if len(q.Grep) > MaxLogGrep {
		return q, Errorf(CodeInvalid, "grep is at most %d bytes", MaxLogGrep)
	}
	if strings.ContainsRune(q.Grep, 0) {
		return q, Errorf(CodeInvalid, "grep contains a NUL byte")
	}
	if q.Offset < 0 {
		return q, Errorf(CodeInvalid, "offset must not be negative")
	}
	if _, err := ParseLogSince(q.Since, time.Now()); err != nil {
		return q, err
	}
	return q, nil
}

// ParseLogQuery reads a JailLogQuery from a query string.
func ParseLogQuery(v url.Values) (JailLogQuery, error) {
	var q JailLogQuery
	if s := v.Get("tail"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			return q, Errorf(CodeInvalid, "tail must be a number of lines between 1 and %d", MaxLogTail)
		}
		q.Tail = n
	}
	if s := v.Get("offset"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return q, Errorf(CodeInvalid, "offset must be a byte offset")
		}
		q.Offset = n
	}
	q.Since, q.Grep = v.Get("since"), v.Get("grep")
	return q.Normalize()
}

// Values is the query string of q; defaults are left out.
func (q JailLogQuery) Values() url.Values {
	v := url.Values{}
	if q.Tail > 0 && q.Tail != DefaultLogTail {
		v.Set("tail", strconv.Itoa(q.Tail))
	}
	if q.Since != "" {
		v.Set("since", q.Since)
	}
	if q.Grep != "" {
		v.Set("grep", q.Grep)
	}
	if q.Offset > 0 {
		v.Set("offset", strconv.FormatInt(q.Offset, 10))
	}
	return v
}

// ParseLogSince turns a since value into a time: an RFC 3339 time as it is,
// a duration ("15m", "2h30m", "3d") subtracted from now. Empty is the zero time.
func ParseLogSince(s string, now time.Time) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	d := s
	if n, ok := strings.CutSuffix(d, "d"); ok {
		if days, err := strconv.Atoi(n); err == nil && days >= 0 {
			d = strconv.Itoa(days*24) + "h"
		}
	}
	if dur, err := time.ParseDuration(d); err == nil && dur >= 0 {
		return now.Add(-dur), nil
	}
	return time.Time{}, Errorf(CodeInvalid, "since must be an RFC 3339 time or a duration such as 15m, 2h or 1d")
}

// LogLineTime reads the timestamp a log line starts with: RFC 3339, a date
// and time ("2026-10-29 12:34:56"), the syslog form ("Oct 29 12:34:56", in
// the year of now or the one before) or a bracketed web server form
// ("[29/Oct/2026:12:34:56 +0000]"). Forms without a zone are read in now's.
func LogLineTime(line string, now time.Time) (time.Time, bool) {
	loc := now.Location()
	if i := strings.IndexByte(line, ' '); i >= 19 {
		if t, err := time.Parse(time.RFC3339Nano, line[:i]); err == nil {
			return t, true
		}
	}
	if len(line) >= 19 {
		if t, err := time.ParseInLocation("2006-01-02 15:04:05", line[:19], loc); err == nil {
			return t, true
		}
	}
	if len(line) >= 15 {
		if t, err := time.ParseInLocation("Jan _2 15:04:05", line[:15], loc); err == nil {
			t = t.AddDate(now.Year(), 0, 0)
			if t.After(now.Add(24 * time.Hour)) {
				t = t.AddDate(-1, 0, 0)
			}
			return t, true
		}
	}
	if i := strings.IndexByte(line, '['); i >= 0 && i < 80 && len(line) > i+21 {
		rest := line[i+1:]
		if j := strings.IndexByte(rest, ']'); j > 0 {
			if t, err := time.Parse("02/Jan/2006:15:04:05 -0700", rest[:j]); err == nil {
				return t, true
			}
			if t, err := time.ParseInLocation("02/Jan/2006:15:04:05", rest[:j], loc); err == nil {
				return t, true
			}
		}
	}
	return time.Time{}, false
}

// LogWindowStart is where a read of a file of size bytes begins: at offset,
// or later when more than max bytes follow it. An offset beyond the size
// (the file was rotated) starts over.
func LogWindowStart(size, offset, limit int64) int64 {
	if offset > size {
		offset = 0
	}
	return max(offset, size-limit)
}

// AssembleLogRead builds the reply from the bytes read: window holds the
// file from start to size. The query's filters are applied in the order
// since, grep, tail; a partial first line (the window did not begin where
// the caller asked) is dropped.
func AssembleLogRead(jail, file string, window []byte, start, size int64, q JailLogQuery, now time.Time) JailLogRead {
	out := JailLogRead{Jail: jail, File: file, Lines: []JailLogLine{}, NextOffset: size}
	offset := q.Offset
	if offset > size {
		out.Rotated, offset = true, 0
	}
	raw := bytes.Split(window, []byte("\n"))
	if len(raw) > 0 && len(raw[len(raw)-1]) == 0 {
		raw = raw[:len(raw)-1] // the newline that ends the last line
	}
	if start > offset {
		out.Truncated = true
		if len(raw) > 0 {
			raw = raw[1:] // a line that began before the window
		}
	}
	since, _ := ParseLogSince(q.Since, now)
	grep := strings.ToLower(q.Grep)
	var last time.Time
	known := false
	for i, b := range raw {
		text := string(b)
		if !since.IsZero() {
			if t, ok := LogLineTime(text, now); ok {
				last, known = t, true
			}
			if known && last.Before(since) {
				continue
			}
		}
		if grep != "" && !strings.Contains(strings.ToLower(text), grep) {
			continue
		}
		l := JailLogLine{Text: text}
		if start == 0 {
			l.N = int64(i) + 1
		}
		out.Lines = append(out.Lines, l)
	}
	if q.Tail > 0 && len(out.Lines) > q.Tail {
		out.Lines = out.Lines[len(out.Lines)-q.Tail:]
		out.Truncated = true
	}
	return out
}
