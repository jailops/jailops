package render

import (
	"bytes"
	"encoding/json"

	"jailops.net/jailops/sdk/api"
)

// Canonical encodes v as indented JSON with object keys in sorted order at
// every level, no HTML escaping and a trailing newline, so that equal values
// always produce identical bytes.
func Canonical(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	// encoding/json writes map keys in sorted order.
	if err := enc.Encode(tree); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func utc(m api.ObjectMeta) api.ObjectMeta {
	if !m.CreatedAt.IsZero() {
		m.CreatedAt = m.CreatedAt.UTC()
	}
	if !m.UpdatedAt.IsZero() {
		m.UpdatedAt = m.UpdatedAt.UTC()
	}
	return m
}

// SpecJSON renders spec.json of a jail: the document envelope with metadata
// and spec. Status is never written; desired state holds no observed data.
func SpecJSON(j api.Jail) ([]byte, error) {
	return Canonical(api.Document{
		APIVersion: api.APIVersion,
		Kind:       api.KindJail,
		Metadata:   utc(j.Metadata),
		Spec:       j.Spec,
	})
}

// BundleJSON renders bundle.json, the restore closure of a jail.
func BundleJSON(b api.RestoreBundle) ([]byte, error) { return Canonical(b) }
