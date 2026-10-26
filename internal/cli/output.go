package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

type format string

const (
	fmtTable format = "table"
	fmtJSON  format = "json"
	fmtYAML  format = "yaml"
	fmtName  format = "name"
)

func parseFormat(s string) (format, error) {
	switch format(s) {
	case "", fmtTable:
		return fmtTable, nil
	case fmtJSON, fmtYAML, fmtName:
		return format(s), nil
	}
	return "", usagef("unknown output format %q (use table, json, yaml or name)", s)
}

// styleFn returns an ANSI SGR code ("32") for a table cell, or "" for none.
type styleFn func(col int, val string) string

// emit prints v in the selected format. table builds the human form and names
// the list of names for -o name; either may be nil.
func (a *app) emit(v any, style styleFn, table func() ([]string, [][]string), names func() []string) error {
	w := a.env.Stdout
	switch a.format {
	case fmtJSON:
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(v); err != nil {
			return err
		}
	case fmtYAML:
		b, err := toYAML(v)
		if err != nil {
			return err
		}
		_, _ = w.Write(b)
	case fmtName:
		if names == nil {
			return usagef("-o name is not available for this command")
		}
		for _, n := range names() {
			fprintln(w, n)
		}
	default:
		if table == nil {
			b, err := toYAML(v)
			if err != nil {
				return err
			}
			_, _ = w.Write(b)
			return nil
		}
		h, rows := table()
		if len(rows) == 0 {
			fprintln(a.env.Stderr, "No resources found.")
			return nil
		}
		a.printTable(w, h, rows, style)
	}
	return nil
}

func (a *app) printTable(w io.Writer, head []string, rows [][]string, style styleFn) {
	color := style != nil && a.colorOn(w)
	width := make([]int, len(head))
	for i, h := range head {
		width[i] = utf8.RuneCountInString(h)
	}
	for _, r := range rows {
		for i, c := range r {
			if i < len(width) {
				width[i] = max(width[i], utf8.RuneCountInString(c))
			}
		}
	}
	line := func(cells []string, hdr bool) {
		var b strings.Builder
		for i, c := range cells {
			cell := c
			pad := width[i] - utf8.RuneCountInString(c)
			if color && !hdr {
				if code := style(i, c); code != "" {
					cell = "\x1b[" + code + "m" + c + "\x1b[0m"
				}
			}
			b.WriteString(cell)
			if i < len(cells)-1 {
				b.WriteString(strings.Repeat(" ", pad+3))
			}
		}
		fprintln(w, b.String())
	}
	if !a.noHeaders {
		line(head, true)
	}
	for _, r := range rows {
		for len(r) < len(head) {
			r = append(r, "")
		}
		line(r, false)
	}
}

// stateStyle colours a power state cell in column col.
func stateStyle(col int) styleFn {
	return func(c int, v string) string {
		if c != col {
			return ""
		}
		switch v {
		case "running", "online", "success", "OK", "ok", "PASS":
			return "32"
		case "stopped", "pending", "offline":
			return "90"
		case "starting", "stopping", "running...", "WARN":
			return "33"
		case "error", "failure", "FAIL":
			return "31"
		}
		return ""
	}
}

// toYAML encodes any JSON-serialisable value as YAML with the field order and
// names of its JSON form (the wire types carry json tags only).
func toYAML(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	n, err := jsonToNode(raw)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(n); err != nil {
		return nil, err
	}
	_ = enc.Close()
	return buf.Bytes(), nil
}

func jsonToNode(raw []byte) (*yaml.Node, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return decodeNode(dec)
}

func decodeNode(dec *json.Decoder) (*yaml.Node, error) {
	t, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch tv := t.(type) {
	case json.Delim:
		switch tv {
		case '{':
			n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				val, err := decodeNode(dec)
				if err != nil {
					return nil, err
				}
				if val.Kind == yaml.MappingNode && len(val.Content) == 0 {
					continue // an empty object adds nothing to the human form
				}
				n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: kt.(string)}, val)
			}
			_, err = dec.Token()
			return n, err
		case '[':
			n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
			for dec.More() {
				val, err := decodeNode(dec)
				if err != nil {
					return nil, err
				}
				n.Content = append(n.Content, val)
			}
			_, err = dec.Token()
			return n, err
		}
	case string:
		n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: tv}
		if strings.Contains(tv, "\n") {
			n.Style = yaml.LiteralStyle
		}
		return n, nil
	case json.Number:
		tag := "!!int"
		if strings.ContainsAny(tv.String(), ".eE") {
			tag = "!!float"
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: tv.String()}, nil
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: fmt.Sprint(tv)}, nil
	case nil:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}, nil
	}
	return nil, fmt.Errorf("unexpected JSON token %v", t)
}

// humanization -------------------------------------------------------------

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func age(now, t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := now.Sub(t)
	switch {
	case d < 0:
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func joinOrDash(s []string) string { return dash(strings.Join(s, ",")) }
