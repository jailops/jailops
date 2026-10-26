package server

import (
	"context"
	"io"
	"log/slog"
	"regexp"
	"strings"
)

var secretValue = regexp.MustCompile(`jo[tjc]_[A-Za-z0-9_.-]{16,}`)

var secretKeys = []string{"password", "passwd", "secret", "token", "authorization", "cookie", "csrf", "totp", "recovery", "private", "key"}

// NewLogger builds the daemon logger: structured, to w, secrets redacted. The
// text format is one key=value line per record, which rc.d and syslog take as is.
func NewLogger(w io.Writer, format string, level slog.Level) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if format == "json" {
		h = slog.NewJSONHandler(w, opts)
	} else {
		h = slog.NewTextHandler(w, opts)
	}
	return slog.New(&redactHandler{h})
}

// redactHandler masks credential-looking attributes and token-shaped values
// before any record reaches the output.
type redactHandler struct{ slog.Handler }

func redactAttr(a slog.Attr) slog.Attr {
	a.Value = a.Value.Resolve()
	switch a.Value.Kind() {
	case slog.KindGroup:
		g := a.Value.Group()
		out := make([]slog.Attr, len(g))
		for i, x := range g {
			out[i] = redactAttr(x)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}
	case slog.KindString:
		k := strings.ToLower(a.Key)
		for _, s := range secretKeys {
			if strings.Contains(k, s) && !strings.HasSuffix(k, "_id") && k != "keyring" {
				return slog.String(a.Key, "[redacted]")
			}
		}
		return slog.String(a.Key, secretValue.ReplaceAllString(a.Value.String(), "[redacted]"))
	case slog.KindAny:
		if err, ok := a.Value.Any().(error); ok {
			return slog.String(a.Key, secretValue.ReplaceAllString(err.Error(), "[redacted]"))
		}
	}
	return a
}

func (h *redactHandler) Handle(ctx context.Context, r slog.Record) error {
	r.Message = secretValue.ReplaceAllString(r.Message, "[redacted]")
	var attrs []slog.Attr
	r.Attrs(func(a slog.Attr) bool { attrs = append(attrs, redactAttr(a)); return true })
	nr := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	nr.AddAttrs(attrs...)
	return h.Handler.Handle(ctx, nr)
}

func (h *redactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		out[i] = redactAttr(a)
	}
	return &redactHandler{h.Handler.WithAttrs(out)}
}

func (h *redactHandler) WithGroup(name string) slog.Handler {
	return &redactHandler{h.Handler.WithGroup(name)}
}
