package lifecycle

import (
	"bytes"
	"encoding/json"

	"jailops.net/jailops/sdk/api"
)

// upgradeSpec rewrites a spec.json or bundle.json written by an earlier release into the current shapes:
//
//   - an image reference with an alias ("freebsd-15-1@stable", written before image aliases were removed) becomes the bare
//     image name: the reference every upgrade, update and image listing resolves is "name" or "name#gN";
//   - the proxy devices written before the exposure of jails (spec.expose) become expose entries: a proxy device forwarded
//     one IPv4 port, so it becomes one IPv4 entry with a host port.
//
// A document that needs neither is returned as it is.
func upgradeSpec(b []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var doc map[string]any
	if dec.Decode(&doc) != nil {
		return b
	}
	spec, ok := doc["spec"].(map[string]any)
	if !ok {
		return b
	}
	if len(api.UpgradeSpec(api.KindJail, spec)) == 0 {
		return b
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return b
	}
	return out
}
