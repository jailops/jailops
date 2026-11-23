package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"jailops.net/jailops/internal/plan"
	"jailops.net/jailops/sdk/api"
)

// UpgradeRequest is api.JailUpgrade plus a Stamp that names the plan's
// snapshots. Persist the request with the journal record and pass the same
// Stamp when rebuilding the plan for crash recovery; empty means "now".
type UpgradeRequest struct {
	api.JailUpgrade
	Stamp string
	// Resume rebuilds the plan after a crash from the state recorded by the
	// original run (UpgradeResult.State). The pre-flight checks are skipped; use it only to finish an interrupted run.
	Resume *SwitchState
	// Ports is the upgrade of the ports packages of a jail inside the running jail, which the service decides
	// and runs (it knows pkg -j): the plan takes it into the same transaction as the base. Never persisted.
	Ports *PortsUpgrade `json:"-"`
}

// PortsUpgrade is the ports part of a jail's upgrade. Pending are the names of the packages with a newer
// version; Run upgrades them inside the running jail. A rebuilt plan (Resume) has no Run: it only undoes.
type PortsUpgrade struct {
	Pending []string
	Run     func(ctx context.Context) error
	// None says the jail has no ports packages: there is no ports step and no ports catalogue was fetched.
	None bool
}

// SwitchState is what a plan needs that the pool no longer shows once the
// plan has started. Persist it with the journal record.
type SwitchState struct {
	WasRunning bool `json:"was_running"`
	// Snapshot is the pre-upgrade snapshot a rollback returns to: a rebuilt plan of an interrupted run needs it also
	// when the run destroyed it already.
	Snapshot string `json:"snapshot,omitempty"`
	// BaseChange and PortsStep say what the plan of an in-place upgrade holds, so that a rebuilt plan has the same
	// steps.
	BaseChange bool `json:"base_change,omitempty"`
	PortsStep  bool `json:"ports_step,omitempty"`
}

// UpgradeResult is the outcome of an upgrade (or a root rollback).
type UpgradeResult struct {
	// Changed is false when there was nothing to upgrade.
	Changed  bool
	From, To string
	Plan     []string
	// Snapshot is the atomic pre-upgrade snapshot (the rollback point).
	Snapshot string
	Warnings []string
	// Stamp names the plan's snapshots; Finish uses it.
	Stamp string
	// State must be persisted with the journal record, see SwitchState.
	State SwitchState
	// FromRelease and ToRelease are the release installed in the jail and the one the upgrade goes to ("15.1-RELEASE").
	FromRelease, ToRelease string
	// Kept names what the upgrade keeps, for the plan a dry run shows.
	Kept []string
	// Nothing says the jail has nothing to upgrade; Installed is the release installed in it ("15.1-RELEASE-p7").
	Nothing   bool
	Installed string
	// InstalledAfter is the release the upgraded root carries, read while no jail ran in it (verify-release): what
	// PropFreeBSDVersion records when the upgrade succeeds.
	InstalledAfter string
	// RestartPending are the processes of a jail that still run files the upgrade replaced ("cron (pid 812)"):
	// they use the new code once they are restarted, which JailOps never does on its own.
	RestartPending []string
	// BasePending and PortsPending are the packages with a newer version (the base ones by name).
	BasePending, PortsPending []string
	// BaseChanges are the base packages that change, "name old -> new"; Checks are the pre-flight checks that passed,
	// each with what it looked at; WillStop says whether the jail is stopped; HealthCheck is the command that decides.
	BaseChanges, Checks []string
	WillStop            bool
	HealthCheck         string
	// Failed are the pre-flight checks that failed in a dry run (a real run stops at the first); Download is the size in
	// bytes of the packages to fetch (0 when unknown), DiskDelta how much more the root takes once they are installed,
	// Pkgsave the *.pkgsave files the upgrade left under etc/ and usr/local/etc/.
	Failed              []string
	Download, DiskDelta int64
	Pkgsave             []string
}

func (s *Service) stamp(req string) string {
	if req != "" {
		return req
	}
	return strconv.FormatInt(s.Cfg.now().Unix(), 10)
}

func genName(image string, n int) string { return fmt.Sprintf("%s#g%d", image, n) }

// UpgradeJail upgrades a jail in place (planThickUpgrade), or reports Changed=false when there is nothing newer.
func (s *Service) UpgradeJail(ctx context.Context, name string, req UpgradeRequest) (UpgradeResult, error) {
	p, res, err := s.PlanUpgrade(ctx, name, req)
	if err != nil {
		return *res, err
	}
	if p == nil || req.DryRun {
		return *res, nil
	}
	if err = s.run(ctx, "upgrade", name, p); err != nil {
		s.NoteUpgradeFailure(ctx, name, err)
		return *res, err
	}
	s.FinishSwitch(ctx, name, res)
	return *res, nil
}

// FinishSwitch is the best-effort work after a successful upgrade or root rollback: the incomplete mark is cleared
// and the release the upgrade installed is recorded. A caller that runs the plan itself must call it after the plan
// succeeds.
func (s *Service) FinishSwitch(ctx context.Context, name string, res *UpgradeResult) {
	st, err := s.observe(ctx, name)
	if err != nil {
		res.Warnings = append(res.Warnings, err.Error())
		return
	}
	s.clearUpgradeIncomplete(ctx, st.Container)
	if res.InstalledAfter != "" {
		// the release a jail is at is the one its upgrade installed, read before the jail ran again
		_ = s.ZFS.Set(context.WithoutCancel(ctx), st.Root, map[string]string{PropFreeBSDVersion: res.InstalledAfter})
	}
}

// PlanUpgrade runs the pre-flight checks and builds the plan of an in-place upgrade (upgrade_thick.go). The plan is
// nil when there is nothing to do.
func (s *Service) PlanUpgrade(ctx context.Context, name string, req UpgradeRequest) (*plan.Plan, *UpgradeResult, error) {
	res := &UpgradeResult{}
	st, err := s.own(ctx, name)
	if err != nil {
		return nil, res, err
	}
	// the upgrade ends with a health check: a locked jail cannot have one (a rebuilt plan only finishes)
	if req.Resume == nil {
		if err := s.refuseLocked(ctx, st); err != nil {
			return nil, res, err
		}
	}
	rec, err := s.readRecord(ctx, name)
	if err != nil {
		return nil, res, fmt.Errorf("read spec.json: %w", err)
	}
	return s.planThickUpgrade(ctx, st, rec, req, res)
}

// plainName is the name of a jail without its project.
func plainName(key string) string {
	if _, n := splitKey(key); n != "" {
		return n
	}
	return key
}

// HealthError is the failure of the health step: the jail does not run, or its health check command failed. Msg is
// the text of the error; Command and Output (the output of the command) are empty for a jail that does not run.
type HealthError struct {
	Msg, Command, Output string
	Err                  error
}

func (e *HealthError) Error() string { return e.Msg }
func (e *HealthError) Unwrap() error { return e.Err }

// healthStep requires the jail to run and its health check command to succeed; on describes where, for the errors.
func (s *Service) healthStep(name string, rec Record, on string) plan.Step {
	hc := strings.Fields(rec.Spec.Update.Healthcheck)
	return plan.Step{Name: "health-check", Describe: "jexec " + name + " " + strings.Join(hc, " ") + " (and require the jail to run)",
		Do: func(ctx context.Context) error {
			if up, err := s.Jails.Running(ctx, name); err != nil {
				return err
			} else if !up {
				return &HealthError{Msg: fmt.Sprintf("jail %s is not running %s", name, on)}
			}
			if len(hc) == 0 {
				return nil
			}
			out, err := s.Jails.Exec(ctx, name, hc)
			if err != nil {
				cmd, text := strings.Join(hc, " "), strings.TrimSpace(string(out))
				return &HealthError{Msg: fmt.Sprintf("health check %q failed: %v: %s", cmd, err, text), Command: cmd, Output: text, Err: err}
			}
			return nil
		}}
}

func (s *Service) destroySnaps(snaps []string) func(context.Context) error {
	return func(ctx context.Context) error {
		var errs []error
		for _, sn := range snaps {
			if ok, _ := s.ZFS.Exists(ctx, sn); !ok {
				continue
			}
			if err := s.ZFS.Destroy(ctx, sn); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}
}

func parseRel(r string) (int, int, error) {
	v, _, _ := strings.Cut(r, "-")
	a, b, ok := strings.Cut(v, ".")
	if !ok {
		return 0, 0, fmt.Errorf("release %q is not <major>.<minor>-<type>", r)
	}
	ma, err := strconv.Atoi(a)
	if err != nil {
		return 0, 0, fmt.Errorf("release %q: %w", r, err)
	}
	mi, err := strconv.Atoi(b)
	if err != nil {
		return 0, 0, fmt.Errorf("release %q: %w", r, err)
	}
	return ma, mi, nil
}

// releaseNewer reports whether image is newer than host.
func releaseNewer(image, host string) (bool, error) {
	a, b, err := parseRel(image)
	if err != nil {
		return false, err
	}
	c, d, err := parseRel(host)
	if err != nil {
		return false, err
	}
	return a > c || (a == c && b > d), nil
}

// CleanupRootOld destroys the snapshots of upgrades and the root.old a 0.9.2 conversion to thick left once their
// retention has passed. It returns whether it destroyed anything. It never touches a root.old without the cleanup
// mark.
func (s *Service) CleanupRootOld(ctx context.Context, name string) (bool, error) {
	st, err := s.observe(ctx, name)
	if err != nil || st.Unsupported {
		return false, err
	}
	if gone, err := s.cleanupUpgradeSnapshots(ctx, st); err != nil || gone {
		return gone, err
	}
	if st.RootOld == "" {
		return false, nil
	}
	mark := s.prop(ctx, st.RootOld, PropCleanupAfter)
	if mark == "" || s.prop(ctx, st.RootOld, PropManaged) != "on" {
		return false, nil
	}
	due, err := time.Parse(time.RFC3339, mark)
	if err != nil {
		return false, api.Errorf(api.CodeInternal, "bad %s on %s: %v", PropCleanupAfter, st.RootOld, err)
	}
	if s.Cfg.now().Before(due) {
		return false, nil
	}
	if err := s.ZFS.DestroyRecursive(ctx, st.RootOld); err != nil {
		return false, err
	}
	return true, nil
}
