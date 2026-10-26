package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"jailops.net/jailops/sdk/api"
)

// readInput reads a file, or standard input for "-".
func (a *app) readInput(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(a.in)
	}
	return os.ReadFile(path)
}

// readDocs splits YAML (or JSON) input into documents.
func readDocs(b []byte) ([]map[string]any, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	var out []map[string]any
	for {
		var m map[string]any
		err := dec.Decode(&m)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, usagef("invalid YAML: %v", err)
		}
		if m != nil {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		return nil, usagef("the input holds no document")
	}
	return out, nil
}

// decodeStrict decodes generic YAML data into v and rejects unknown fields,
// so a misspelt key is an error instead of being ignored.
func decodeStrict(data any, v any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return usagef("%s", cleanJSONErr(err))
	}
	return nil
}

func cleanJSONErr(err error) string {
	s := err.Error()
	s = strings.ReplaceAll(s, "json: unknown field", "unknown field")
	s = strings.ReplaceAll(s, "json: cannot unmarshal", "cannot use")
	return s
}

// parseObject reads one document as either the declarative envelope
// (apiVersion, kind, metadata, spec) or a bare spec, and returns the metadata
// and the strictly decoded spec. nameHint names a bare spec; kind is the kind
// the caller expects.
func parseObject[S any](doc map[string]any, kind api.Kind, nameHint string) (api.ObjectMeta, S, error) {
	var meta api.ObjectMeta
	var spec S
	_, hasKind := doc["kind"]
	_, hasAPI := doc["apiVersion"]
	_, hasSpec := doc["spec"]
	_, hasMeta := doc["metadata"]
	if !(hasKind || hasAPI || hasSpec || hasMeta) {
		// A bare spec.
		if nameHint == "" {
			return meta, spec, usagef("a bare spec needs a name: pass NAME on the command line")
		}
		meta.Name = nameHint
		return meta, spec, decodeStrict(doc, &spec)
	}
	for k := range doc {
		switch k {
		case "apiVersion", "kind", "metadata", "spec", "status":
		default:
			return meta, spec, usagef("unknown field %q in document (expected apiVersion, kind, metadata, spec)", k)
		}
	}
	if v, ok := doc["apiVersion"].(string); ok && v != api.APIVersion {
		return meta, spec, usagef("unsupported apiVersion %q (this client speaks %s)", v, api.APIVersion)
	}
	if k, ok := doc["kind"].(string); ok && api.Kind(k) != kind {
		return meta, spec, usagef("the document is a %s, but this command handles %s", k, kind)
	}
	if hasMeta {
		if err := decodeStrict(doc["metadata"], &meta); err != nil {
			return meta, spec, fmt.Errorf("metadata: %w", err)
		}
	}
	if nameHint != "" {
		if meta.Name != "" && meta.Name != nameHint {
			return meta, spec, usagef("the document is named %q but the command line says %q", meta.Name, nameHint)
		}
		meta.Name = nameHint
	}
	if meta.Name == "" {
		return meta, spec, usagef("metadata.name is required")
	}
	if hasSpec {
		if err := decodeStrict(doc["spec"], &spec); err != nil {
			return meta, spec, fmt.Errorf("spec: %w", err)
		}
	}
	return meta, spec, nil
}

// document renders an object as the declarative envelope. Only the name and
// labels of the metadata are included: the rest is owned by the server.
func document(kind api.Kind, meta api.ObjectMeta, spec any) any {
	type m struct {
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels,omitempty"`
	}
	return struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   m      `json:"metadata"`
		Spec       any    `json:"spec"`
	}{api.APIVersion, string(kind), m{meta.Name, meta.Labels}, spec}
}
