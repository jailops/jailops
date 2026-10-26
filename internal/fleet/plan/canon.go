package plan

import "encoding/json"

// jsonCanon marshals v; encoding/json sorts map keys, so the output is stable.
func jsonCanon(v any) ([]byte, error) { return json.Marshal(v) }
