package fake

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"jailops.net/jailops/sdk/api"
)

// Encrypted volumes (extension volume_unlock): the fake keeps the passphrase of each jail with encrypted volumes,
// starts it unlocked, and locks it again on RebootHost. Five wrong passphrases in a row lock unlocks out for 30
// seconds of the fake's clock, as the daemon does. The request log never holds a passphrase: secretFields are
// replaced before a body is recorded.
type fakeVolumes struct {
	pass  map[string]string // by jail key
	fails map[string]int
	until map[string]time.Time
}

func (v *fakeVolumes) init() {
	if v.pass == nil {
		v.pass, v.fails, v.until = map[string]string{}, map[string]int{}, map[string]time.Time{}
	}
}

// secretFields are the request fields that carry a passphrase.
var secretFields = []string{"volume_passphrase", "passphrase", "old_passphrase", "new_passphrase"}

// redactBody replaces the passphrases of a JSON request body.
func redactBody(b []byte) []byte {
	if len(b) == 0 || !strings.Contains(string(b), "passphrase") {
		return b
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return []byte(`"unreadable body withheld"`)
	}
	for _, f := range secretFields {
		if _, ok := m[f]; ok {
			m[f] = json.RawMessage(`"redacted"`)
		}
	}
	out, _ := json.Marshal(m)
	return out
}

func encryptedVolumes(j *api.Jail) []string {
	var out []string
	for _, v := range j.Spec.Volumes {
		if v.Encrypted {
			out = append(out, v.Name)
		}
	}
	return out
}

// jailCreate is POST /api/v1/jails: a Jail with the passphrase of its encrypted volumes.
func (s *Server) jailCreate(w http.ResponseWriter, r *http.Request) {
	in, e := decode[api.JailCreate](r)
	if e != nil {
		writeErr(w, e)
		return
	}
	j := api.Jail{Metadata: in.Metadata, Spec: in.Spec}
	enc := len(encryptedVolumes(&j)) > 0
	switch {
	case !enc && in.VolumePassphrase != "":
		writeErr(w, api.Errorf(api.CodeInvalid, "volume_passphrase was given but no volume is encrypted"))
		return
	case enc && in.VolumePassphrase == "":
		writeErr(w, api.Errorf(api.CodeInvalid, "a jail with an encrypted volume needs volume_passphrase"))
		return
	case enc:
		if err := api.CheckPassphrase(in.VolumePassphrase); err != nil {
			writeErr(w, err.(*api.Error))
			return
		}
	}
	rp := s.jails.doCreate(r, &j)
	if rp.err == nil && enc {
		s.mu.Lock()
		s.vol.init()
		s.vol.pass[s.jails.key(r, j.Metadata.Name)] = in.VolumePassphrase
		s.mu.Unlock()
	}
	s.respond(w, rp)
}

// RebootHost stops every jail and locks those with encrypted volumes, as a host boot leaves them.
func (s *Server) RebootHost() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jails.items {
		j.Status.Power, j.Status.JID, j.Status.StartedAt, j.Status.Addresses = api.PowerStopped, 0, time.Time{}, nil
		if l := encryptedVolumes(j); len(l) > 0 {
			j.Status.Power, j.Status.Locked = api.PowerLocked, l
		}
	}
}

func (s *Server) volLockedOut(key string) *api.Error {
	s.vol.init()
	if t, ok := s.vol.until[key]; ok {
		if s.now().Before(t) {
			return api.Errorf(api.CodeRateLimited, "too many wrong passphrases for this jail").WithHint("wait 30s and try again")
		}
		delete(s.vol.until, key)
	}
	return nil
}

func (s *Server) volResult(key string, ok bool) {
	if ok {
		delete(s.vol.fails, key)
		return
	}
	s.vol.fails[key]++
	if s.vol.fails[key] >= 5 {
		delete(s.vol.fails, key)
		s.vol.until[key] = s.now().Add(30 * time.Second)
	}
}

// keyedJail finds a jail with encrypted volumes for a key request. s.mu is held.
func (s *Server) keyedJail(r *http.Request, name string) (string, *api.Jail, *api.Error) {
	key, j := s.jails.find(r, name)
	if j == nil {
		return "", nil, notFound("jail", name)
	}
	if len(encryptedVolumes(j)) == 0 {
		return "", nil, api.Errorf(api.CodeInvalid, "jail %q has no encrypted volume", name)
	}
	return key, j, nil
}

func (s *Server) jailUnlock(w http.ResponseWriter, r *http.Request) {
	u, e := decode[api.JailUnlock](r)
	if e != nil {
		writeErr(w, e)
		return
	}
	name, rc := r.PathValue("name"), s.info(r)
	s.mu.Lock()
	rp := func() reply {
		key, _, e := s.keyedJail(r, name)
		if e != nil {
			return reply{err: e}
		}
		if u.Passphrase == "" {
			return reply{err: api.Errorf(api.CodeInvalid, "passphrase must be 1 to 512 bytes without a line break")}
		}
		if e := s.volLockedOut(key); e != nil {
			return reply{err: e}
		}
		op := s.startOp(rc.project, "jails.unlock", "Unlock jail "+name, map[string][]string{"jails": {"/api/v1/jails/" + name}}, func() (any, *api.Error) {
			j := s.jails.items[key]
			if j == nil {
				return nil, notFound("jail", name)
			}
			ok := s.vol.pass[key] == u.Passphrase
			s.volResult(key, ok)
			if !ok {
				return nil, api.Errorf(api.CodeBadPassphrase, "the passphrase is not correct")
			}
			j.Status.Locked = nil
			if j.Status.Power == api.PowerLocked {
				j.Status.Power = api.PowerStopped
			}
			s.emitIn(rc.project, "jail-unlocked", "/api/v1/jails/"+name)
			if u.Start && j.Status.Power != api.PowerRunning {
				s.nextJID++
				j.Status.Power, j.Status.JID, j.Status.StartedAt = api.PowerRunning, s.nextJID, s.now()
				s.emitIn(rc.project, "jail-started", "/api/v1/jails/"+name)
			}
			return clone(j), nil
		})
		return reply{op: &op}
	}()
	s.mu.Unlock()
	s.respond(w, rp)
}

func (s *Server) jailLock(w http.ResponseWriter, r *http.Request) {
	name, rc := r.PathValue("name"), s.info(r)
	s.mu.Lock()
	rp := func() reply {
		key, _, e := s.keyedJail(r, name)
		if e != nil {
			return reply{err: e}
		}
		op := s.startOp(rc.project, "jails.lock", "Lock jail "+name, map[string][]string{"jails": {"/api/v1/jails/" + name}}, func() (any, *api.Error) {
			j := s.jails.items[key]
			if j == nil {
				return nil, notFound("jail", name)
			}
			j.Status.JID, j.Status.StartedAt, j.Status.Addresses = 0, time.Time{}, nil
			j.Status.Power, j.Status.Locked = api.PowerLocked, encryptedVolumes(j)
			s.emitIn(rc.project, "jail-locked", "/api/v1/jails/"+name)
			return clone(j), nil
		})
		return reply{op: &op}
	}()
	s.mu.Unlock()
	s.respond(w, rp)
}

func (s *Server) jailPassphrase(w http.ResponseWriter, r *http.Request) {
	pc, e := decode[api.JailPassphraseChange](r)
	if e != nil {
		writeErr(w, e)
		return
	}
	name, rc := r.PathValue("name"), s.info(r)
	s.mu.Lock()
	rp := func() reply {
		key, j, e := s.keyedJail(r, name)
		if e != nil {
			return reply{err: e}
		}
		if err := api.CheckPassphrase(pc.New); err != nil {
			return reply{err: err.(*api.Error)}
		}
		if len(j.Status.Locked) > 0 {
			return reply{err: api.Errorf(api.CodeLocked, "jail %q is locked", name).WithHint("jailops jail unlock " + name)}
		}
		if e := s.volLockedOut(key); e != nil {
			return reply{err: e}
		}
		op := s.startOp(rc.project, "jails.passphrase", "Change the volume passphrase of jail "+name, map[string][]string{"jails": {"/api/v1/jails/" + name}}, func() (any, *api.Error) {
			ok := s.vol.pass[key] == pc.Old
			s.volResult(key, ok)
			if !ok {
				return nil, api.Errorf(api.CodeBadPassphrase, "the passphrase is not correct")
			}
			s.vol.pass[key] = pc.New
			return clone(s.jails.items[key]), nil
		})
		return reply{op: &op}
	}()
	s.mu.Unlock()
	s.respond(w, rp)
}
