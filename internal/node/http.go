package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"jailops.net/jailops/internal/node/ctl"
	"jailops.net/jailops/internal/node/wire"
	"jailops.net/jailops/internal/repo/datastore"
	"jailops.net/jailops/internal/repo/format"
	"jailops.net/jailops/sdk/api"
)

// Handler serves HostRoutes. The caller authenticates the connection (mutual
// TLS, pinned fingerprints) and attaches the host with WithHost; a request
// without one is refused.
func (n *Node) Handler() http.Handler {
	mux := http.NewServeMux()
	h := map[string]func(http.ResponseWriter, *http.Request, Host){
		"GET " + Base:                                               n.hInfo,
		"POST " + Base + "/sessions":                                n.hBegin,
		"POST " + Base + "/sessions/{sid}/missing":                  n.hMissing,
		"PUT " + Base + "/sessions/{sid}/chunks/{id}":               n.hPutChunk,
		"PUT " + Base + "/sessions/{sid}/index":                     n.hPutIndex,
		"PUT " + Base + "/sessions/{sid}/manifest":                  n.hPutManifest,
		"POST " + Base + "/sessions/{sid}/commit":                   n.hCommit,
		"POST " + Base + "/sessions/{sid}/heartbeat":                n.hHeartbeat,
		"DELETE " + Base + "/sessions/{sid}":                        n.hAbort,
		"GET " + Base + "/groups":                                   n.hGroups,
		"GET " + Base + "/groups/{group}/snapshots":                 n.hSnapshots,
		"GET " + Base + "/groups/{group}/snapshots/{snap}":          n.hSnapshot,
		"GET " + Base + "/groups/{group}/snapshots/{snap}/index":    n.hFile(FileIndex),
		"GET " + Base + "/groups/{group}/snapshots/{snap}/receipt":  n.hFile(FileReceipt),
		"GET " + Base + "/groups/{group}/snapshots/{snap}/manifest": n.hFile(FileManifest),
		"GET " + Base + "/chunks/{id}":                              n.hGetChunk,
		"POST " + Base + "/restore-tests":                           n.hRestoreTest,
		"GET " + Base + "/escrow":                                   n.hListEscrow,
		"GET " + Base + "/escrow/{domain}":                          n.hGetEscrow,
		"PUT " + Base + "/escrow/{domain}":                          n.hPutEscrow,
	}
	for _, rt := range HostRoutes {
		fn, ok := h[rt.Key()]
		if !ok {
			panic("node: no handler for " + rt.Key())
		}
		delete(h, rt.Key())
		perm := rt.Perm
		mux.HandleFunc(rt.Key(), func(w http.ResponseWriter, r *http.Request) {
			host, ok := HostFrom(r.Context())
			if !ok {
				writeErr(w, errf(api.CodeUnauthorized, "no authenticated host"))
				return
			}
			allowed := false
			for _, s := range host.Scopes {
				if Satisfies(api.Permission(s), perm) {
					allowed = true
					break
				}
			}
			if !allowed {
				writeErr(w, errf(api.CodeForbidden, "the host lacks the %s scope", perm))
				return
			}
			fn(w, r, host)
		})
	}
	if len(h) != 0 {
		panic(fmt.Sprintf("node: %d handlers have no route", len(h)))
	}
	return mux
}

// ---- envelope ---------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	var meta json.RawMessage
	if v != nil {
		b, err := json.Marshal(v)
		if err != nil {
			writeErr(w, errf(api.CodeInternal, "encoding the answer: %v", err))
			return
		}
		meta = b
	}
	resp := api.Response{Type: api.ResponseSync, Status: http.StatusText(status), StatusCode: status, Metadata: meta}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}

func writeErr(w http.ResponseWriter, err error) {
	ae := asAPIError(err)
	status := ae.Code.HTTPStatus()
	resp := api.Response{Type: api.ResponseError, Status: http.StatusText(status), StatusCode: status, Error: ae}
	w.Header().Set("Content-Type", "application/json")
	if ae.Code == api.CodeMaintenance || ae.Code == api.CodeBusy {
		w.Header().Set("Retry-After", "30")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 8<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return errf(api.CodeInvalid, "malformed request body: %v", err)
	}
	return nil
}

// bodySize requires a Content-Length: the node verifies sizes, it does not guess them.
func bodySize(r *http.Request) (int64, error) {
	if r.ContentLength < 0 {
		return 0, errf(api.CodeInvalid, "a Content-Length is required")
	}
	return r.ContentLength, nil
}

func parseID(s string) (format.ID, error) {
	id, err := format.ParseID(s)
	if err != nil {
		return id, errf(api.CodeInvalid, "malformed chunk id")
	}
	return id, nil
}

// ---- host handlers ---------------------------------------------------------------

func (n *Node) hInfo(w http.ResponseWriter, r *http.Request, h Host) { writeJSON(w, 200, n.Info()) }

// BeginRequest is the body of POST /api/v1/repo/sessions.
type BeginRequest = wire.BeginRequest

func (n *Node) hBegin(w http.ResponseWriter, r *http.Request, h Host) {
	var req BeginRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	si, err := n.BeginWith(r.Context(), h, req)
	if err != nil {
		writeErr(w, err)
		return
	}
	n.db.TouchHost(r.Context(), h.ID)
	writeJSON(w, http.StatusCreated, si)
}

// MissingRequest is the body of POST .../missing.
type MissingRequest = wire.MissingRequest

// MissingResponse lists the ids the repository lacks.
type MissingResponse = wire.MissingResponse

func (n *Node) hMissing(w http.ResponseWriter, r *http.Request, h Host) {
	var req MissingRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if len(req.IDs) > MaxMissingBatch {
		writeErr(w, errf(api.CodeInvalid, "at most %d ids per request", MaxMissingBatch))
		return
	}
	ids := make([]format.ID, len(req.IDs))
	for i, s := range req.IDs {
		id, err := parseID(s)
		if err != nil {
			writeErr(w, err)
			return
		}
		ids[i] = id
	}
	miss, err := n.Missing(r.Context(), h, r.PathValue("sid"), ids)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := MissingResponse{Missing: make([]string, len(miss))}
	for i, id := range miss {
		out.Missing[i] = id.String()
	}
	writeJSON(w, 200, out)
}

func (n *Node) hPutChunk(w http.ResponseWriter, r *http.Request, h Host) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	size, err := bodySize(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	res, err := n.PutChunk(r.Context(), h, r.PathValue("sid"), id, r.Body, size)
	if err != nil {
		writeErr(w, err)
		return
	}
	status := http.StatusOK
	if res.Created {
		status = http.StatusCreated
	}
	writeJSON(w, status, res)
}

// IndexResponse answers an index upload.
type IndexResponse = wire.IndexResponse

func (n *Node) hPutIndex(w http.ResponseWriter, r *http.Request, h Host) {
	size, err := bodySize(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	d, c, err := n.PutIndex(r.Context(), h, r.PathValue("sid"), r.Body, size)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, IndexResponse{Digest: d, Chunks: c})
}

// ManifestResponse answers a manifest upload.
type ManifestResponse = wire.ManifestResponse

func (n *Node) hPutManifest(w http.ResponseWriter, r *http.Request, h Host) {
	size, err := bodySize(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	d, err := n.PutManifest(r.Context(), h, r.PathValue("sid"), r.Body, size)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, ManifestResponse{Digest: d})
}

func (n *Node) hCommit(w http.ResponseWriter, r *http.Request, h Host) {
	var req CommitRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	rc, err := n.Commit(r.Context(), h, r.PathValue("sid"), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, rc)
}

// HeartbeatResponse carries the renewed lease.
type HeartbeatResponse = wire.HeartbeatResponse

func (n *Node) hHeartbeat(w http.ResponseWriter, r *http.Request, h Host) {
	t, err := n.Heartbeat(r.Context(), h, r.PathValue("sid"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, HeartbeatResponse{LeaseUntil: t})
}

func (n *Node) hAbort(w http.ResponseWriter, r *http.Request, h Host) {
	if err := n.Abort(r.Context(), h, r.PathValue("sid")); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, nil)
}

func (n *Node) hGroups(w http.ResponseWriter, r *http.Request, h Host) {
	g, err := n.GroupsOf(r.Context(), h)
	if err != nil {
		writeErr(w, err)
		return
	}
	if g == nil {
		g = []ctl.Group{}
	}
	writeJSON(w, 200, g)
}

func (n *Node) hSnapshots(w http.ResponseWriter, r *http.Request, h Host) {
	group := r.PathValue("group")
	err := n.mayUseGroupID(r.Context(), h, group)
	var s []ctl.Snapshot
	if err == nil {
		s, err = n.Snapshots(r.Context(), group)
	}
	if api.IsCode(err, api.CodeNotFound) { // a group that is not the host's reads as one with no snapshots (a first backup lists it)
		s, err = nil, nil
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	if s == nil {
		s = []ctl.Snapshot{}
	}
	writeJSON(w, 200, s)
}

func (n *Node) hSnapshot(w http.ResponseWriter, r *http.Request, h Host) {
	group := r.PathValue("group")
	err := n.mayUseGroupID(r.Context(), h, group)
	var s ctl.Snapshot
	if err == nil {
		s, err = n.SnapshotState(r.Context(), group, r.PathValue("snap"))
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, s)
}

// parseRange handles "bytes=a-b" and "bytes=a-" (one range, no suffix form).
func parseRange(h string) (off, length int64, ok bool, err error) {
	if h == "" {
		return 0, -1, false, nil
	}
	spec, found := strings.CutPrefix(h, "bytes=")
	a, b, found2 := strings.Cut(spec, "-")
	if !found || !found2 || a == "" || strings.Contains(spec, ",") {
		return 0, 0, false, errf(api.CodeInvalid, "only a single bytes=a-b or bytes=a- range is supported")
	}
	off, perr := strconv.ParseInt(a, 10, 64)
	if perr != nil || off < 0 {
		return 0, 0, false, errf(api.CodeInvalid, "malformed range")
	}
	if b == "" {
		return off, -1, true, nil
	}
	last, perr := strconv.ParseInt(b, 10, 64)
	if perr != nil || last < off {
		return 0, 0, false, errf(api.CodeInvalid, "malformed range")
	}
	return off, last - off + 1, true, nil
}

func serveObject(w http.ResponseWriter, r *http.Request, open func(off, length int64) (*Object, error)) {
	off, length, ranged, err := parseRange(r.Header.Get("Range"))
	if err != nil {
		writeErr(w, err)
		return
	}
	o, err := open(off, length)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer o.Close()
	hd := w.Header()
	hd.Set("Content-Type", "application/octet-stream")
	hd.Set("Accept-Ranges", "bytes")
	hd.Set("Content-Length", strconv.FormatInt(o.Length, 10))
	status := http.StatusOK
	if ranged {
		status = http.StatusPartialContent
		hd.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", o.Offset, o.Offset+o.Length-1, o.Size))
	}
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = io.CopyN(w, o, o.Length)
	}
}

func (n *Node) hFile(f SnapshotFile) func(http.ResponseWriter, *http.Request, Host) {
	return func(w http.ResponseWriter, r *http.Request, h Host) {
		serveObject(w, r, func(off, length int64) (*Object, error) {
			if err := n.mayUseGroupID(r.Context(), h, r.PathValue("group")); err != nil {
				return nil, err
			}
			return n.OpenSnapshotFile(r.Context(), r.PathValue("group"), r.PathValue("snap"), f, off, length)
		})
	}
}

func (n *Node) hGetChunk(w http.ResponseWriter, r *http.Request, h Host) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	serveObject(w, r, func(off, length int64) (*Object, error) { return n.OpenChunk(r.Context(), id, off, length) })
}

// RestoreTest is the report of a restore test run on a host that holds the key.
type RestoreTest = wire.RestoreTest

func (n *Node) hRestoreTest(w http.ResponseWriter, r *http.Request, h Host) {
	var rt RestoreTest
	if err := decode(r, &rt); err != nil {
		writeErr(w, err)
		return
	}
	if err := n.RecordRestoreTest(r.Context(), h, rt); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, nil)
}

// RecordRestoreTest stores the report of a restore test as a job and, when the
// test found damage, marks the snapshot failed: a failed snapshot is never a base.
func (n *Node) RecordRestoreTest(ctx context.Context, h Host, rt RestoreTest) error {
	if err := checkIDs(rt.Group, rt.Snapshot); err != nil {
		return err
	}
	// Before anything is written: the group exists and is the host's (or the host is a recovery host), and the
	// snapshot is a committed one. A report about something else would be a job row for nothing, or a way for any
	// host to mark another host's snapshot failed.
	if err := n.mayUseGroup(ctx, h, rt.Group); err != nil {
		return err
	}
	if _, err := n.be.Head(ctx, snapPrefix(rt.Group, rt.Snapshot)+fileCommit); err != nil {
		return asAPIError(err)
	}
	switch rt.Method {
	case "deep", "extract", "full":
	default:
		return errf(api.CodeInvalid, "method must be deep, extract or full")
	}
	if len(rt.Problems) > 100 {
		rt.Problems = rt.Problems[:100]
	}
	job, err := n.db.StartJob(ctx, ctl.JobRestoreTest, h.Name, rt)
	if err != nil {
		return err
	}
	state := ctl.JobSucceeded
	var jerr error
	if !rt.OK {
		state, jerr = ctl.JobFailed, errors.New("the restore test found damage")
	}
	if err := n.db.FinishJob(ctx, job.ID, state, rt, jerr); err != nil {
		return err
	}
	if !rt.OK {
		if _, err := n.be.Head(ctx, snapPrefix(rt.Group, rt.Snapshot)+fileCommit); err == nil {
			n.recordVerify(ctx, SnapshotResult{Group: rt.Group, Snapshot: rt.Snapshot, State: VerifyFailed, Chunks: rt.Chunks, Problems: rt.Problems}, n.now())
		} else if !errors.Is(err, datastore.ErrNotFound) {
			return asAPIError(err)
		}
	}
	return nil
}

func itoa(n int) string { return strconv.Itoa(n) }
