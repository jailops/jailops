package fake

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

// The export of the fake carries the manifest and no data; the import makes the jail of the manifest. The file is a
// real export file of the format, so the client's checks (a cut-off file, a file that is not one) are the real ones.

func exportRecord(typ byte, payload []byte) []byte {
	h := make([]byte, 9, 9+len(payload))
	h[0] = typ
	binary.BigEndian.PutUint64(h[1:], uint64(len(payload)))
	return append(h, payload...)
}

// jailExport answers GET /jails/{name}/export.
func (s *Server) jailExport(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	_, j := s.jails.find(r, r.PathValue("name"))
	var c api.Jail
	if j != nil {
		c = clone(j)
	}
	s.mu.Unlock()
	if j == nil {
		writeErr(w, notFound("jail", r.PathValue("name")))
		return
	}
	m := api.ExportManifest{Format: api.ExportFormat, Created: s.now().UTC(), Name: c.Metadata.Name, Project: c.Metadata.Project,
		UUID: c.Metadata.UUID, Layout: c.Spec.Layout, Image: api.ExportImage{Name: c.Spec.Image},
		Bundle: api.RestoreBundle{Spec: c.Spec}, Parts: []api.ExportPart{}}
	if r.URL.Query().Get("with_volumes") == "true" {
		m.Warnings = append(m.Warnings, "the fake carries no volumes")
	}
	b, _ := json.Marshal(m)
	roll := sha256.Sum256(b)
	foot, _ := json.Marshal(map[string]any{"parts": 0, "sha256": hex.EncodeToString(roll[:])})
	w.Header().Set("Content-Type", "application/vnd.jailops.export")
	w.Header().Set("Trailer", "X-Jailops-Export")
	_, _ = io.WriteString(w, sdk.ExportMagic)
	_, _ = w.Write(exportRecord(sdk.RecManifest, b))
	_, _ = w.Write(exportRecord(sdk.RecFooter, foot))
	w.Header().Set("X-Jailops-Export", "ok")
}

func (s *Server) importErrors(r *http.Request, m api.ExportManifest, req api.JailImportRequest) []string {
	rc := s.info(r)
	name := req.As
	if name == "" {
		name = m.Name
	}
	var errs []string
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.jails.items[s.jails.keyIn(rc.project, name)] != nil {
		errs = append(errs, "a jail named "+name+" exists: choose another name with --as")
	}
	return errs
}

// jailImportPreflight answers POST /jail-imports/preflight.
func (s *Server) jailImportPreflight(w http.ResponseWriter, r *http.Request) {
	in, e := decode[api.JailImportPreflight](r)
	if e != nil {
		writeErr(w, e)
		return
	}
	errs := s.importErrors(r, in.Manifest, in.Request)
	pf := api.RestorePreflight{OK: len(errs) == 0, Errors: errs, Changes: []string{"the new jail gets a new uuid and does not start at boot"}}
	writeSync(w, http.StatusOK, pf)
}

// jailImport answers POST /jail-imports.
func (s *Server) jailImport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeErr(w, api.Errorf(api.CodeInvalid, "reading the file: %v", err))
		return
	}
	m, err := sdk.ReadExportManifest(bytes.NewReader(body))
	if err == nil {
		_, err = sdk.ScanExport(bytes.NewReader(body))
	}
	if err != nil {
		writeErr(w, api.Errorf(api.CodeInvalid, "%v", err))
		return
	}
	req := api.JailImportRequest{As: q.Get("as"), KeepIP: q.Get("keep_ip") == "true", Start: q.Get("start") == "true"}
	if errs := s.importErrors(r, m, req); len(errs) > 0 {
		writeErr(w, api.Errorf(api.CodeConflict, "%s", errs[0]))
		return
	}
	rc := s.info(r)
	j := api.Jail{Metadata: api.ObjectMeta{Name: m.Name, Project: rc.project, Labels: m.Labels}, Spec: m.Bundle.Spec}
	if req.As != "" {
		j.Metadata.Name = req.As
	}
	j.Spec.Boot.Autostart = false
	s.mu.Lock()
	s.jails.insert(&j, false)
	c := clone(&j)
	s.mu.Unlock()
	writeSync(w, http.StatusCreated, api.JailImported{Jail: c})
}
