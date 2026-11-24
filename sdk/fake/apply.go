package fake

import (
	"encoding/json"

	"jailops.net/jailops/sdk/api"
)

// The fake keeps what a recipe applied to a jail, step by step, so that a second apply of the same step is unchanged
// and a dry run says so, as a daemon does. It reads no jail: "done" is the memory of the fake.

// ApplyCall is one request of POST /api/v1/jails/{name}/apply.
type ApplyCall struct {
	Project, Jail string
	Req           api.ApplyRequest
}

type applyFault struct {
	step int
	msg  string
}

type fakeApply struct {
	done  map[string]map[string]bool // jail key -> steps done
	fault map[string]applyFault      // jail key -> the next real apply fails at a step
	calls []ApplyCall
}

// SeedApplyFailure makes the next real apply of a recipe to the jail fail at step (counted from 0, the steps before it
// are done, as a daemon leaves them), once. An empty msg removes a failure that has not happened yet.
func (s *Server) SeedApplyFailure(project, jail string, step int, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := project + "/" + jail
	if s.apply.fault == nil {
		s.apply.fault = map[string]applyFault{}
	}
	if msg == "" {
		delete(s.apply.fault, k)
		return
	}
	s.apply.fault[k] = applyFault{step, msg}
}

// SeedApplyDrift forgets what was applied to the jail: a person changed it, and a dry run finds every step to change.
func (s *Server) SeedApplyDrift(project, jail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.apply.done, project+"/"+jail)
}

// ApplyCalls returns the apply requests the server received, in order.
func (s *Server) ApplyCalls() []ApplyCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ApplyCall(nil), s.apply.calls...)
}

// stepKey identifies a step by its content.
func stepKey(st api.RecipeStep) string {
	b, _ := json.Marshal(st)
	return string(b)
}
