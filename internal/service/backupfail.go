package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"jailops.net/jailops/sdk/api"
)

// backupFailure is a scheduled backup that did not work.
type backupFailure struct {
	Project, Policy, Jail, Err string
	At                         time.Time
}

// BackupResult is told by the scheduler how a scheduled backup ended. A failure is kept for jailops doctor and sent as
// an event (backup-failed); the next backup of that policy and jail that works clears it.
func (s *Service) BackupResult(project, policy, jail string, err error) {
	key := project + "/" + policy + "/" + jail
	s.backupMu.Lock()
	if s.backupFails == nil {
		s.backupFails = map[string]backupFailure{}
	}
	if err == nil {
		delete(s.backupFails, key)
		s.backupMu.Unlock()
		return
	}
	msg := firstLine(err.Error())
	s.backupFails[key] = backupFailure{Project: project, Policy: policy, Jail: jail, Err: msg, At: s.now()}
	s.backupMu.Unlock()
	s.emitIn(context.Background(), project, "backup-failed", "/api/v1/backup/policies/"+policy)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// doctorBackups warns for each policy whose last scheduled backup failed.
func (s *Service) doctorBackups(c *checkSet) {
	s.backupMu.Lock()
	var fails []backupFailure
	for _, f := range s.backupFails {
		fails = append(fails, f)
	}
	s.backupMu.Unlock()
	if len(fails) == 0 {
		return
	}
	sort.Slice(fails, func(i, j int) bool { return fails[i].Policy+fails[i].Jail < fails[j].Policy+fails[j].Jail })
	var l []string
	for _, f := range fails {
		l = append(l, fmt.Sprintf("policy %s, jail %s (%s ago): %s", f.Policy, f.Jail, s.now().Sub(f.At).Round(time.Minute), f.Err))
	}
	c.add("backup.recent", api.DoctorWarn, "the last scheduled backup failed: "+strings.Join(l, "; "),
		"jailops backup run POLICY tries again; jailops backup list shows what exists; the daemon's log has the whole error")
}
