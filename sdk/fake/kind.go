package fake

import (
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
)

import "jailops.net/jailops/sdk/api"

// kind is the generic CRUD implementation of one resource collection.
type kind[T any] struct {
	s      *Server
	plural string // "jails"
	base   string // "/api/v1/jails"
	action string // "jail" in lifecycle actions
	scope  api.Scope
	items  map[string]*T
	meta   func(*T) *api.ObjectMeta

	keep         func(dst, old *T)            // copy server-owned fields (status) on update
	validate     func(obj, old *T) *api.Error // read-only checks
	assign       func(obj, old *T) *api.Error // mutating step (IPAM, defaults), runs where the object is stored
	beforeDelete func(obj *T) *api.Error
	onDelete     func(obj *T)
	decorate     func(obj *T) // fills computed status on copies
	created      func(obj *T) // fills what only the answer of a create carries (warnings), on that copy

	asyncCreate, asyncDelete bool
	readonly, noCreate       bool
	// noUpdate leaves out PUT and PATCH (tokens are created and revoked, never edited).
	noUpdate bool

	// scoped kinds are stored per project (key "<project>/<name>").
	scoped bool
	// crossProject kinds are host-wide but carry a project in their metadata
	// (backups); ?project= narrows their lists.
	crossProject bool
	// visible hides objects from a caller (not_found); nil shows all.
	visible func(rc *reqInfo, obj *T) bool
	// alias maps another way of naming an object (a fingerprint) to its name.
	alias func(key string) string
	// guard vets a write by the request's identity.
	guard func(rc *reqInfo, obj *T, verb string) *api.Error
	// intercept may answer a GET itself; it reports whether it did.
	intercept func(w http.ResponseWriter, r *http.Request) bool
	// after runs after an object is stored (create or update), s.mu held.
	after func(obj *T)
	// expand answers GET ?expanded=true.
	expand func(obj *T) any
}

// key is the map key of a name in the project of the request.
func (k *kind[T]) key(r *http.Request, name string) string {
	if !k.scoped {
		return name
	}
	return k.s.info(r).project + "/" + name
}

func (k *kind[T]) keyIn(project, name string) string {
	if !k.scoped {
		return name
	}
	return project + "/" + name
}

// find resolves a path name to a stored object under s.mu.
func (k *kind[T]) find(r *http.Request, name string) (string, *T) {
	key := k.key(r, name)
	o := k.items[key]
	if o == nil && k.alias != nil {
		if n := k.alias(name); n != "" {
			key = k.keyIn(k.s.info(r).project, n)
			o = k.items[key]
		}
	}
	if o != nil && k.visible != nil && !k.visible(k.s.info(r), o) {
		return key, nil
	}
	return key, o
}

// proj is the project events and operations of obj belong to.
func (k *kind[T]) proj(obj *T) string {
	if k.scoped {
		return k.meta(obj).Project
	}
	return ""
}

func newKind[T any](s *Server, plural, action string, scope api.Scope, meta func(*T) *api.ObjectMeta) *kind[T] {
	return &kind[T]{s: s, plural: plural, base: "/api/v1/" + plural, action: action, scope: scope, items: map[string]*T{}, meta: meta}
}

func clone[T any](v *T) T {
	var out T
	b, _ := json.Marshal(v)
	_ = json.Unmarshal(b, &out)
	return out
}

func etagOf(gen int64) string { return `"` + strconv.FormatInt(gen, 10) + `"` }

type reply struct {
	status int
	meta   any
	hdr    map[string]string
	err    *api.Error
	op     *api.Operation
}

func (s *Server) respond(w http.ResponseWriter, r reply) {
	for k, v := range r.hdr {
		w.Header().Set(k, v)
	}
	switch {
	case r.err != nil:
		writeErr(w, r.err)
	case r.op != nil:
		raw, _ := json.Marshal(r.op)
		w.Header().Set("Location", "/api/v1/operations/"+r.op.ID)
		writeJSON(w, http.StatusAccepted, api.Response{Type: api.ResponseAsync, Status: "Operation created",
			StatusCode: 202, Operation: "/api/v1/operations/" + r.op.ID, Metadata: raw})
	default:
		writeSync(w, r.status, r.meta)
	}
}

func (k *kind[T]) register(mux muxer) {
	mux("GET "+k.base, k.list)
	mux("GET "+k.base+"/{name}", k.get)
	mux("DELETE "+k.base+"/{name}", k.del)
	if k.readonly {
		return
	}
	if !k.noCreate {
		mux("POST "+k.base, k.create)
	}
	if k.noUpdate {
		return
	}
	mux("PUT "+k.base+"/{name}", k.put)
	mux("PATCH "+k.base+"/{name}", k.patch)
}

func validName(n string) bool {
	if n == "" || len(n) > 128 {
		return false
	}
	for _, c := range n {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

func (k *kind[T]) list(w http.ResponseWriter, r *http.Request) {
	want := map[string]string{}
	for _, l := range r.URL.Query()["label"] {
		kk, v, _ := strings.Cut(l, "=")
		want[kk] = v
	}
	rc := k.s.info(r)
	k.s.mu.Lock()
	names := make([]string, 0, len(k.items))
	for n := range k.items {
		names = append(names, n)
	}
	sort.Strings(names)
	out := []T{}
	for _, n := range names {
		o := k.items[n]
		m := k.meta(o)
		if k.scoped && m.Project != rc.project {
			continue
		}
		if k.crossProject {
			if q := r.URL.Query().Get("project"); q != "" && projectOr(m.Project) != q {
				continue
			}
		}
		if k.visible != nil && !k.visible(rc, o) {
			continue
		}
		labels := m.Labels
		match := true
		for kk, v := range want {
			if lv, ok := labels[kk]; !ok || lv != v {
				match = false
			}
		}
		if match {
			c := clone(o)
			if k.decorate != nil {
				k.decorate(&c)
			}
			out = append(out, c)
		}
	}
	k.s.mu.Unlock()
	k.s.respond(w, reply{status: 200, meta: out})
}

func (k *kind[T]) get(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if k.intercept != nil && k.intercept(w, r) {
		return
	}
	k.s.mu.Lock()
	_, o := k.find(r, name)
	var c T
	var gen int64
	if o != nil {
		c = clone(o)
		gen = k.meta(o).Generation
		if k.decorate != nil {
			k.decorate(&c)
		}
	}
	var ex any
	if o != nil && k.expand != nil && r.URL.Query().Get("expanded") == "true" {
		ex = k.expand(&c)
	}
	k.s.mu.Unlock()
	if o == nil {
		k.s.respond(w, reply{err: notFound(k.action, name)})
		return
	}
	if ex != nil {
		k.s.respond(w, reply{status: 200, meta: ex, hdr: map[string]string{"ETag": etagOf(gen)}})
		return
	}
	k.s.respond(w, reply{status: 200, meta: c, hdr: map[string]string{"ETag": etagOf(gen)}})
}

func notFound(what, name string) *api.Error {
	return api.Errorf(api.CodeNotFound, "%s %q not found", what, name)
}

func decode[T any](r *http.Request) (*T, *api.Error) {
	var v T
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		return nil, api.Errorf(api.CodeInvalid, "invalid request body: %v", err)
	}
	return &v, nil
}

// insert stores obj, filling identity. s.mu is held.
func (k *kind[T]) insert(obj *T, quiet bool) {
	m := k.meta(obj)
	now := k.s.now()
	if m.UUID == "" {
		m.UUID = newUUID()
	}
	m.Scope = k.scope
	if k.scope == api.ScopeHost {
		m.Host = k.s.uuid
	}
	if m.Generation == 0 {
		m.Generation = 1
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = now
	}
	m.UpdatedAt = now
	k.items[k.keyIn(m.Project, m.Name)] = obj
	if !quiet {
		k.s.emitIn(k.proj(obj), k.action+"-created", k.base+"/"+m.Name)
	}
}

func (k *kind[T]) create(w http.ResponseWriter, r *http.Request) {
	obj, e := decode[T](r)
	if e != nil {
		k.s.respond(w, reply{err: e})
		return
	}
	k.s.respond(w, k.doCreate(r, obj))
}

func (k *kind[T]) doCreate(r *http.Request, obj *T) reply {
	s := k.s
	rc := s.info(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	m := k.meta(obj)
	name := m.Name
	if !validName(name) {
		return reply{err: api.Errorf(api.CodeInvalid, "invalid name %q", name).WithHint("use lowercase letters, digits, '-', '_' and '.'")}
	}
	if k.scoped {
		if m.Project != "" && m.Project != rc.project {
			return reply{err: api.Errorf(api.CodeInvalid, "metadata.project %q does not match the request's project %q", m.Project, rc.project)}
		}
		m.Project = rc.project
	}
	if k.guard != nil {
		if e := k.guard(rc, obj, "create"); e != nil {
			return reply{err: e}
		}
	}
	if k.items[k.keyIn(m.Project, name)] != nil {
		return reply{err: api.Errorf(api.CodeAlreadyExists, "%s %q already exists", k.action, name)}
	}
	if k.validate != nil {
		if e := k.validate(obj, nil); e != nil {
			return reply{err: e}
		}
	}
	store := func() (any, *api.Error) {
		if k.items[k.keyIn(m.Project, name)] != nil {
			return nil, api.Errorf(api.CodeAlreadyExists, "%s %q already exists", k.action, name)
		}
		if k.assign != nil {
			if e := k.assign(obj, nil); e != nil {
				return nil, e
			}
		}
		k.insert(obj, false)
		if k.after != nil {
			k.after(obj)
		}
		c := clone(obj)
		if k.decorate != nil {
			k.decorate(&c)
		}
		if k.created != nil {
			k.created(&c)
		}
		return c, nil
	}
	if k.asyncCreate {
		op := s.startOp(k.proj(obj), k.plural+".create", "Creating "+k.action+" "+name, map[string][]string{k.plural: {k.base + "/" + name}}, store)
		return reply{op: &op}
	}
	res, e := store()
	if e != nil {
		return reply{err: e}
	}
	return reply{status: 201, meta: res, hdr: map[string]string{"Location": k.base + "/" + name, "ETag": etagOf(k.meta(obj).Generation)}}
}

func ifMatchOK(r *http.Request, gen int64) bool {
	im := strings.TrimSpace(r.Header.Get("If-Match"))
	if im == "" || im == "*" {
		return true
	}
	return slices.ContainsFunc(strings.Split(im, ","), func(p string) bool { return strings.TrimSpace(p) == etagOf(gen) })
}

func (k *kind[T]) put(w http.ResponseWriter, r *http.Request) {
	obj, e := decode[T](r)
	if e != nil {
		k.s.respond(w, reply{err: e})
		return
	}
	k.s.respond(w, k.replace(r, r.PathValue("name"), func(*T) (*T, *api.Error) { return obj, nil }))
}

func (k *kind[T]) patch(w http.ResponseWriter, r *http.Request) {
	var patch map[string]any
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		k.s.respond(w, reply{err: api.Errorf(api.CodeInvalid, "invalid patch: %v", err)})
		return
	}
	k.s.respond(w, k.replace(r, r.PathValue("name"), func(old *T) (*T, *api.Error) {
		var cur map[string]any
		b, _ := json.Marshal(old)
		_ = json.Unmarshal(b, &cur)
		b, _ = json.Marshal(mergePatch(cur, patch))
		out := new(T)
		if err := json.Unmarshal(b, out); err != nil {
			return nil, api.Errorf(api.CodeInvalid, "patched object is invalid: %v", err)
		}
		return out, nil
	}))
}

func mergePatch(dst, patch map[string]any) map[string]any {
	for key, v := range patch {
		switch pv := v.(type) {
		case nil:
			delete(dst, key)
		case map[string]any:
			dm, _ := dst[key].(map[string]any)
			if dm == nil {
				dm = map[string]any{}
			}
			dst[key] = mergePatch(dm, pv)
		default:
			dst[key] = v
		}
	}
	return dst
}

func (k *kind[T]) replace(r *http.Request, name string, build func(old *T) (*T, *api.Error)) reply {
	s := k.s
	rc := s.info(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	key, old := k.find(r, name)
	if old == nil {
		return reply{err: notFound(k.action, name)}
	}
	om := k.meta(old)
	name = om.Name
	if !ifMatchOK(r, om.Generation) {
		return reply{err: api.Errorf(api.CodeConflict, "%s %q changed: current generation is %d", k.action, name, om.Generation).
			WithHint("read it again and re-apply your change")}
	}
	obj, e := build(old)
	if e != nil {
		return reply{err: e}
	}
	m := k.meta(obj)
	if m.Name == "" {
		m.Name = name
	}
	if m.Name != name {
		return reply{err: api.Errorf(api.CodeInvalid, "renaming is not supported")}
	}
	if k.scoped && m.Project != "" && m.Project != om.Project {
		return reply{err: api.Errorf(api.CodeInvalid, "metadata.project cannot change")}
	}
	m.UUID, m.Host, m.Scope, m.CreatedAt, m.Project = om.UUID, om.Host, om.Scope, om.CreatedAt, om.Project
	if k.guard != nil {
		if e := k.guard(rc, old, "update"); e != nil {
			return reply{err: e}
		}
		if e := k.guard(rc, obj, "update"); e != nil {
			return reply{err: e}
		}
	}
	if k.keep != nil {
		k.keep(obj, old)
	}
	if k.validate != nil {
		if e := k.validate(obj, old); e != nil {
			return reply{err: e}
		}
	}
	if k.assign != nil {
		if e := k.assign(obj, old); e != nil {
			return reply{err: e}
		}
	}
	m.Generation, m.UpdatedAt = om.Generation, om.UpdatedAt
	a, _ := json.Marshal(obj)
	b, _ := json.Marshal(old)
	if string(a) != string(b) {
		m.Generation = om.Generation + 1
		m.UpdatedAt = s.now()
		k.items[key] = obj
		s.emitIn(k.proj(obj), k.action+"-updated", k.base+"/"+name)
		if k.after != nil {
			k.after(obj)
		}
	} else {
		obj = old
	}
	c := clone(obj)
	if k.decorate != nil {
		k.decorate(&c)
	}
	return reply{status: 200, meta: c, hdr: map[string]string{"ETag": etagOf(k.meta(obj).Generation)}}
}

func (k *kind[T]) del(w http.ResponseWriter, r *http.Request) {
	s := k.s
	name := r.PathValue("name")
	rc := s.info(r)
	s.mu.Lock()
	rp := func() reply {
		key, o := k.find(r, name)
		if o == nil {
			return reply{err: notFound(k.action, name)}
		}
		name = k.meta(o).Name
		if k.guard != nil {
			if e := k.guard(rc, o, "delete"); e != nil {
				return reply{err: e}
			}
		}
		if k.beforeDelete != nil {
			if e := k.beforeDelete(o); e != nil {
				return reply{err: e}
			}
		}
		remove := func() (any, *api.Error) {
			if k.items[key] == nil {
				return nil, notFound(k.action, name)
			}
			if k.onDelete != nil {
				k.onDelete(k.items[key])
			}
			delete(k.items, key)
			s.emitIn(k.proj(o), k.action+"-deleted", k.base+"/"+name)
			return nil, nil
		}
		if k.asyncDelete || k.readonly {
			op := s.startOp(k.proj(o), k.plural+".delete", "Deleting "+k.action+" "+name, map[string][]string{k.plural: {k.base + "/" + name}}, remove)
			return reply{op: &op}
		}
		if _, e := remove(); e != nil {
			return reply{err: e}
		}
		return reply{status: 200, meta: struct{}{}}
	}()
	s.mu.Unlock()
	s.respond(w, rp)
}

func projectOr(p string) string { return p }
