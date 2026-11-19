package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"sync"
	"time"

	"jailops.net/jailops/sdk/api"
)

// An Idempotency-Key makes a creating request safe to repeat: a client that got no answer (a lost reply, a timeout)
// sends the same request with the same key and receives the operation the first one started, not a second jail. The
// key is scoped to the caller and the route, the body must be the same (a key reused for another body is refused), and
// it is remembered for idempotencyTTL. The table is in memory: a daemon that restarted between the two requests has
// forgotten the key, and the repeat then meets the object the first request created (a conflict that names it) or
// starts the operation anew. `X-Request-ID` is a trace id and has no part in this.
const (
	idempotencyTTL = 24 * time.Hour
	idempotencyMax = 4096
)

var idemKeyRe = regexp.MustCompile(`^[A-Za-z0-9._~-]{8,128}$`)

type idemEntry struct {
	hash string
	op   api.Operation
	at   time.Time
}

type idemTable struct {
	mu  sync.Mutex
	m   map[string]idemEntry
	now func() time.Time
}

func newIdemTable() *idemTable { return &idemTable{m: map[string]idemEntry{}, now: time.Now} }

// lookup returns the operation of an earlier request with this scope and key. mismatch is true when the key was used
// for another body.
func (t *idemTable) lookup(scope, hash string) (op api.Operation, found, mismatch bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.m[scope]
	if !ok || t.now().Sub(e.at) > idempotencyTTL {
		delete(t.m, scope)
		return api.Operation{}, false, false
	}
	if e.hash != hash {
		return api.Operation{}, false, true
	}
	return e.op, true, false
}

func (t *idemTable) store(scope, hash string, op api.Operation) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	if len(t.m) >= idempotencyMax {
		for k, e := range t.m {
			if now.Sub(e.at) > idempotencyTTL {
				delete(t.m, k)
			}
		}
		for len(t.m) >= idempotencyMax { // still full: drop the oldest
			var oldest string
			var at time.Time
			for k, e := range t.m {
				if oldest == "" || e.at.Before(at) {
					oldest, at = k, e.at
				}
			}
			delete(t.m, oldest)
		}
	}
	t.m[scope] = idemEntry{hash: hash, op: op, at: now}
}

// idempotent runs start, unless the request carries an Idempotency-Key that an earlier request used: then it returns
// that request's operation. body is what makes two requests the same.
func (c *rc) idempotent(body any, start func() (api.Operation, error)) (api.Operation, error) {
	key := c.r.Header.Get("Idempotency-Key")
	if key == "" || c.s.idem == nil {
		return start()
	}
	if !idemKeyRe.MatchString(key) {
		return api.Operation{}, api.Errorf(api.CodeInvalid, "invalid Idempotency-Key: use 8 to 128 characters of letters, digits and . _ ~ -")
	}
	raw, _ := json.Marshal(body)
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	scope := c.actor.Kind + "/" + c.actor.Name + "|" + c.r.Method + " " + c.rt.pattern + "|" + key
	if op, found, mismatch := c.s.idem.lookup(scope, hash); mismatch {
		return api.Operation{}, api.Errorf(api.CodePreconditionFailed, "the Idempotency-Key was used for another request body").
			WithHint("use a new key for a different request")
	} else if found {
		c.w.Header().Set("Idempotent-Replayed", "true")
		if cur, err := c.s.svc.GetOperation(c.r.Context(), op.ID); err == nil {
			return cur, nil
		}
		return op, nil
	}
	op, err := start()
	if err != nil {
		return op, err
	}
	c.s.idem.store(scope, hash, op)
	return op, nil
}
