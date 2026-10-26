package fake

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"jailops.net/jailops/sdk/api"
)

type opState struct {
	op     api.Operation
	done   chan struct{}
	cancel context.CancelFunc
}

type sseEvent struct {
	id int64
	ev api.Event
}

// startOp registers an operation and runs fn in a goroutine. The caller holds
// s.mu; fn runs with s.mu held. key selects injected failures.
func (s *Server) startOp(project, key, desc string, resources map[string][]string, fn func() (any, *api.Error)) api.Operation {
	id := newUUID()
	ctx, cancel := context.WithCancel(s.baseCtx)
	now := s.now()
	st := &opState{
		op: api.Operation{ID: id, Description: desc, Status: api.OpPending, Resources: resources, Project: project,
			MayCancel: true, CreatedAt: now, UpdatedAt: now},
		done: make(chan struct{}), cancel: cancel,
	}
	s.ops[id] = st
	s.opOrder = append(s.opOrder, id)
	delay, failErr := s.opDelay, s.takeFail(key)
	s.emitOp(st)
	snap := st.op
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		s.mu.Lock()
		if !st.op.Status.Final() {
			st.op.Status = api.OpRunning
			st.op.UpdatedAt = s.now()
			s.emitOp(st)
		}
		s.mu.Unlock()
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-ctx.Done():
			}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if st.op.Status.Final() {
			return
		}
		if ctx.Err() != nil {
			s.finishOp(st, api.OpCancelled, nil, nil)
			return
		}
		if failErr != nil {
			s.finishOp(st, api.OpFailure, nil, failErr)
			return
		}
		res, aerr := fn()
		if aerr != nil {
			s.finishOp(st, api.OpFailure, nil, aerr)
			return
		}
		s.finishOp(st, api.OpSuccess, res, nil)
	}()
	return snap
}

// finishOp ends an operation. s.mu is held.
func (s *Server) finishOp(st *opState, status api.OperationStatus, result any, e *api.Error) {
	if st.op.Status.Final() {
		return
	}
	st.op.Status = status
	st.op.Error = e
	st.op.MayCancel = false
	st.op.UpdatedAt = s.now()
	if status == api.OpSuccess {
		st.op.Progress = 100
		if result != nil {
			st.op.Metadata, _ = json.Marshal(result)
		}
	}
	close(st.done)
	s.emitOp(st)
}

func (s *Server) emitOp(st *opState) {
	raw, _ := json.Marshal(st.op)
	s.publish(api.EventOperation, raw)
}

// emit publishes a lifecycle event. s.mu is held.
func (s *Server) emit(action, source string) { s.emitIn("", action, source) }

// emitIn publishes a lifecycle event of a project ("" for host-level objects).
func (s *Server) emitIn(project, action, source string) {
	raw, _ := json.Marshal(api.LifecycleEvent{Action: action, Source: source, Requestor: "fake", Project: project})
	s.publish(api.EventLifecycle, raw)
}

func (s *Server) publish(t api.EventType, meta json.RawMessage) {
	s.nextEvent++
	s.events = append(s.events, sseEvent{s.nextEvent, api.Event{Type: t, Timestamp: s.now(), Host: s.uuid, Metadata: meta}})
	if len(s.events) > 1000 {
		s.events = s.events[len(s.events)-1000:]
	}
	close(s.notify)
	s.notify = make(chan struct{})
}

// EventStreams reports how many /1.0/events responses are open, so tests can
// wait for a subscriber before causing events.
func (s *Server) EventStreams() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.streams
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, api.Errorf(api.CodeInternal, "streaming unsupported"))
		return
	}
	rc := s.info(r)
	narrow := r.URL.Query().Get("project")
	var types map[string]bool
	if t := r.URL.Query().Get("type"); t != "" {
		types = map[string]bool{}
		for _, p := range strings.Split(t, ",") {
			types[p] = true
		}
	}
	s.mu.Lock()
	last := s.nextEvent
	if h := r.Header.Get("Last-Event-ID"); h != "" {
		if n, err := strconv.ParseInt(h, 10, 64); err == nil {
			last = n
		}
	}
	drop := s.drop
	s.streams++
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.streams--; s.mu.Unlock() }()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	fl.Flush()
	for {
		s.mu.Lock()
		var out []sseEvent
		for _, e := range s.events {
			if e.id > last && (types == nil || types[string(e.ev.Type)]) && eventVisible(rc, e.ev, narrow) {
				out = append(out, e)
			}
		}
		if n := len(s.events); n > 0 && s.events[n-1].id > last {
			last = s.events[n-1].id
		}
		notify := s.notify
		s.mu.Unlock()
		for _, e := range out {
			data, _ := json.Marshal(e.ev)
			fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.id, e.ev.Type, data)
		}
		fl.Flush()
		select {
		case <-notify:
		case <-drop:
			return
		case <-s.closed:
			return
		case <-r.Context().Done():
			return
		case <-time.After(15 * time.Second):
			fmt.Fprint(w, ": ping\n\n")
		}
	}
}

func (s *Server) handleOpList(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	out := []api.Operation{}
	rc := s.info(r)
	narrow := r.URL.Query().Get("project")
	for _, id := range s.opOrder {
		if op := s.ops[id].op; rc.canSee(op.Project) && (narrow == "" || op.Project == narrow) {
			out = append(out, op)
		}
	}
	b, _ := json.Marshal(out)
	s.mu.Unlock()
	writeJSON(w, 200, api.Response{Type: api.ResponseSync, Status: "Success", StatusCode: 200, Metadata: b})
}

func (s *Server) lookupOp(r *http.Request, id string) (*opState, *api.Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.ops[id]
	if st == nil || !s.info(r).canSee(st.op.Project) {
		return nil, api.Errorf(api.CodeNotFound, "operation %q not found", id)
	}
	return st, nil
}

func (s *Server) opSnapshot(st *opState) api.Operation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return st.op
}

func (s *Server) handleOpGet(w http.ResponseWriter, r *http.Request) {
	st, e := s.lookupOp(r, r.PathValue("id"))
	if e != nil {
		writeErr(w, e)
		return
	}
	writeSync(w, 200, s.opSnapshot(st))
}

func (s *Server) handleOpWait(w http.ResponseWriter, r *http.Request) {
	st, e := s.lookupOp(r, r.PathValue("id"))
	if e != nil {
		writeErr(w, e)
		return
	}
	secs := 60
	if n, err := strconv.Atoi(r.URL.Query().Get("timeout")); err == nil && n >= 0 {
		secs = min(n, 300)
	}
	select {
	case <-st.done:
	case <-time.After(time.Duration(secs) * time.Second):
	case <-r.Context().Done():
	case <-s.closed:
	}
	writeSync(w, 200, s.opSnapshot(st))
}

func (s *Server) handleOpCancel(w http.ResponseWriter, r *http.Request) {
	st, e := s.lookupOp(r, r.PathValue("id"))
	if e != nil {
		writeErr(w, e)
		return
	}
	s.mu.Lock()
	if st.op.Status.Final() || !st.op.MayCancel {
		s.mu.Unlock()
		writeErr(w, api.Errorf(api.CodeInvalid, "operation cannot be cancelled"))
		return
	}
	st.cancel()
	s.finishOp(st, api.OpCancelled, nil, nil)
	s.mu.Unlock()
	writeSync(w, 200, struct{}{})
}

// eventVisible says whether the caller may see an event. Events of host-level
// objects, and logging events, are for global identities only.
func eventVisible(rc *reqInfo, ev api.Event, narrow string) bool {
	var m struct {
		Project string `json:"project"`
	}
	if ev.Type != api.EventLogging {
		_ = json.Unmarshal(ev.Metadata, &m)
	}
	if narrow != "" && m.Project != narrow {
		return false
	}
	return rc.canSee(m.Project)
}
