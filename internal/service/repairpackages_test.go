package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"jailops.net/jailops/sdk/api"
)

// A failed pass over the base package records is a BasePackages condition of the jail and a warning of doctor, until
// a pass succeeds; the condition names the command that tries again.
func TestBasePackagesConditionAndDoctor(t *testing.T) {
	s := &Service{now: time.Now, status: map[string]jailRuntime{}, jails: map[string]*jailEntry{
		"alpha_web": {jail: api.Jail{Metadata: api.ObjectMeta{Name: "web", Project: "alpha"}, Spec: api.JailSpec{OS: api.OSFreeBSD}}},
		"alpha_db":  {jail: api.Jail{Metadata: api.ObjectMeta{Name: "db", Project: "alpha"}, Spec: api.JailSpec{OS: api.OSFreeBSD}}},
	}}
	s.setBasePackages(context.Background(), "alpha_web", errors.New("pkg: no space left"))
	s.setBasePackages(context.Background(), "alpha_db", nil)
	c := &checkSet{}
	s.doctorBasePackages(c)
	if len(c.list) != 1 || c.list[0].Status != api.DoctorWarn || !strings.Contains(c.list[0].Message, "alpha/web") || strings.Contains(c.list[0].Message, "alpha/db") ||
		!strings.Contains(c.list[0].Hint, "jailops jail repair-packages") {
		t.Fatalf("%+v", c.list)
	}
	s.mu.RLock()
	msg := s.status["alpha_web"].basePackages
	s.mu.RUnlock()
	if !strings.Contains(msg, "no space left") {
		t.Fatalf("%q", msg)
	}
	s.setBasePackages(context.Background(), "alpha_web", nil)
	c = &checkSet{}
	s.doctorBasePackages(c)
	if c.list[0].Status != api.DoctorOK {
		t.Fatalf("%+v", c.list)
	}
}
