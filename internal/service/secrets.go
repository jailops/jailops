package service

import (
	"bytes"
	"encoding/base64"
	"io"
	"sort"
	"strings"

	"filippo.io/age"
	"filippo.io/age/armor"

	"jailops.net/jailops/sdk/api"
)

// SealedPrefix starts an environment value that is sealed to this host's age recipient. A fleet tool (or any
// client) seals a secret to the recipient the host publishes in its server info; the ciphertext is what is stored and
// returned by the API, and the host opens it only when it starts a process in the jail.
const SealedPrefix = "sealed:age:"

// maxSealed bounds one opened secret.
const maxSealed = 64 << 10

func isSealed(v string) bool { return strings.HasPrefix(v, SealedPrefix) }

// unsealValue opens a sealed value with the host's identity.
func (s *Service) unsealValue(v string) (string, error) {
	if s.d.SecretsIdentity == nil {
		return "", api.Errorf(api.CodeInvalid, "this host holds no secrets key, so it cannot open sealed values")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(v, SealedPrefix))
	if err != nil {
		return "", api.Errorf(api.CodeInvalid, "a sealed value is not valid base64")
	}
	// the fleet tool stores secrets armored; a plain binary age file is accepted too
	var src io.Reader = bytes.NewReader(raw)
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("-----BEGIN AGE ENCRYPTED FILE-----")) {
		src = armor.NewReader(src)
	}
	r, err := age.Decrypt(src, s.d.SecretsIdentity)
	if err != nil {
		return "", api.Errorf(api.CodeInvalid, "a sealed value was not sealed to this host's recipient")
	}
	b, err := io.ReadAll(io.LimitReader(r, maxSealed+1))
	if err != nil || len(b) > maxSealed {
		return "", api.Errorf(api.CodeInvalid, "a sealed value is unreadable or too large")
	}
	return string(b), nil
}

// checkSealedEnv refuses, at admission, a sealed value this host could not open when the jail needs it.
func (s *Service) checkSealedEnv(env map[string]string) error {
	for k, v := range env {
		if !isSealed(v) {
			continue
		}
		if _, err := s.unsealValue(v); err != nil {
			return api.Errorf(api.CodeInvalid, "env %s: %s", k, err.(*api.Error).Message)
		}
	}
	return nil
}

// jailEnv is the environment a process started in the jail gets from the jail's spec: its env values with the
// sealed ones opened. Plaintext lives only in the returned slice.
func (s *Service) jailEnv(env map[string]string) ([]string, error) {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		v := env[k]
		if isSealed(v) {
			var err error
			if v, err = s.unsealValue(v); err != nil {
				return nil, api.Errorf(api.CodeInternal, "env %s: %s", k, err.(*api.Error).Message)
			}
		}
		out = append(out, k+"="+v)
	}
	return out, nil
}
