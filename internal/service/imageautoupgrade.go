package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"jailops.net/jailops/sdk/api"
)

// parseAutoUpgradeAt reads the time of day of an automatic upgrade ("03:00"; empty is the default) as minutes after
// midnight.
func parseAutoUpgradeAt(at string) (int, error) {
	if at == "" {
		at = api.DefaultAutoUpgradeAt
	}
	h, m, ok := strings.Cut(at, ":")
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if !ok || len(h) != 2 || len(m) != 2 || err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, invalid("auto_upgrade.at %q must be a time of day such as 03:00", at)
	}
	return hh*60 + mm, nil
}

// autoUpgradeCatchUp is how long after its time of day a missed automatic upgrade still runs (a host that was off at
// night runs it when it is back); later it waits for the next night.
const autoUpgradeCatchUp = 4 * time.Hour

// autoUpgradeDue says whether an image with auto upgrade should run now: the latest scheduled time (today's, or
// yesterday's when it is earlier than today's has come) has passed, is within the catch-up window and has not run
// yet. last is the start of the latest run, zero when there was none.
func autoUpgradeDue(now, last time.Time, at int) bool {
	slot := scheduledSlot(now, at)
	return !now.Before(slot) && now.Sub(slot) < autoUpgradeCatchUp && last.Before(slot)
}

// scheduledSlot is the latest time of day `at` (minutes after midnight, in now's zone) that is not after now.
func scheduledSlot(now time.Time, at int) time.Time {
	y, mo, d := now.Date()
	slot := time.Date(y, mo, d, at/60, at%60, 0, 0, now.Location())
	if slot.After(now) {
		slot = time.Date(y, mo, d-1, at/60, at%60, 0, 0, now.Location())
	}
	return slot
}

// nextAutoUpgrade is the next time of day `at` after now.
func nextAutoUpgrade(now time.Time, at int) time.Time {
	slot := scheduledSlot(now, at)
	y, mo, d := slot.Date()
	return time.Date(y, mo, d+1, at/60, at%60, 0, 0, now.Location())
}

// maxImageRuns is how many automatic upgrade runs an image keeps.
const maxImageRuns = 30

// imageAutoState is the in-memory part of the automatic upgrade: which images run now, and the runs when the service
// has no state directory (tests).
type imageAutoState struct {
	mu      sync.Mutex
	running map[string]bool
	mem     map[string][]api.ImageUpgradeRun
}

func (s *Service) imageRunsPath(image string) string {
	return filepath.Join(s.d.StateDir, "image-upgrades", image+".json")
}

// imageRuns returns the recorded runs of an image, oldest first.
func (s *Service) imageRuns(image string) []api.ImageUpgradeRun {
	if s.d.StateDir == "" {
		s.imgAuto.mu.Lock()
		defer s.imgAuto.mu.Unlock()
		return append([]api.ImageUpgradeRun(nil), s.imgAuto.mem[image]...)
	}
	b, err := os.ReadFile(s.imageRunsPath(image))
	if err != nil {
		return nil
	}
	var out []api.ImageUpgradeRun
	_ = json.Unmarshal(b, &out)
	return out
}

// appendImageRun records a run, keeping the latest maxImageRuns.
func (s *Service) appendImageRun(image string, r api.ImageUpgradeRun) {
	histMu.Lock()
	defer histMu.Unlock()
	h := append(s.imageRuns(image), r)
	if len(h) > maxImageRuns {
		h = h[len(h)-maxImageRuns:]
	}
	if s.d.StateDir == "" {
		s.imgAuto.mu.Lock()
		if s.imgAuto.mem == nil {
			s.imgAuto.mem = map[string][]api.ImageUpgradeRun{}
		}
		s.imgAuto.mem[image] = h
		s.imgAuto.mu.Unlock()
		return
	}
	path := s.imageRunsPath(image)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		s.log.Warn("the record of an automatic image upgrade cannot be saved", "image", image, "err", err)
		return
	}
	b, _ := json.Marshal(h)
	tmp := path + ".tmp"
	err := os.WriteFile(tmp, b, 0o640)
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		s.log.Warn("the record of an automatic image upgrade cannot be saved", "image", image, "err", err)
	}
}

func (s *Service) autoZone() *time.Location {
	if s.d.AutoUpgradeZone != nil {
		return s.d.AutoUpgradeZone
	}
	return time.Local
}

// autoUpgradeStatus is the ImageStatus.AutoUpgrade of an image: the time of the next run and the latest runs, newest
// first; nil when the image has neither auto upgrade nor a recorded run.
func (s *Service) autoUpgradeStatus(image string, spec api.ImageSpec) *api.ImageAutoUpgradeStatus {
	runs := s.imageRuns(image)
	au := spec.AutoUpgrade
	if (au == nil || !au.Enabled) && len(runs) == 0 {
		return nil
	}
	st := &api.ImageAutoUpgradeStatus{}
	if au != nil && au.Enabled {
		if at, err := parseAutoUpgradeAt(au.At); err == nil {
			st.NextAt = nextAutoUpgrade(s.now().In(s.autoZone()), at).UTC()
		}
	}
	for i := len(runs) - 1; i >= 0 && len(st.Recent) < 10; i-- {
		st.Recent = append(st.Recent, runs[i])
	}
	return st
}

// imageAutoLoop evaluates the automatic upgrades once a tick.
func (s *Service) imageAutoLoop(ctx context.Context) {
	tick := s.d.UpdateTick
	if tick <= 0 {
		tick = time.Minute
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(tick):
		}
		s.runImageAutoUpgrades(ctx)
	}
}

// runImageAutoUpgrades starts the automatic upgrade of every image that is due. One image runs at a time.
func (s *Service) runImageAutoUpgrades(ctx context.Context) {
	if s.requireHost("image upgrades") != nil {
		return
	}
	objs, err := s.imgKind.list()
	if err != nil {
		return
	}
	now := s.now().In(s.autoZone())
	for _, o := range objs {
		au := o.Spec.AutoUpgrade
		if au == nil || !au.Enabled {
			continue
		}
		at, err := parseAutoUpgradeAt(au.At)
		if err != nil {
			continue
		}
		var last time.Time
		if runs := s.imageRuns(o.Meta.Name); len(runs) > 0 {
			last = runs[len(runs)-1].StartedAt
		}
		if !autoUpgradeDue(now, last, at) {
			continue
		}
		s.AutoUpgradeImage(ctx, o.Meta.Name)
	}
}

// AutoUpgradeImage runs the automatic upgrade of one image now, as the system, and records the run: the next
// generation is built. No jail is touched: a jail takes its patches by its own update policy (spec.update). It returns
// after the run; a run already going on for the image is not started again.
func (s *Service) AutoUpgradeImage(ctx context.Context, image string) api.ImageUpgradeRun {
	s.imgAuto.mu.Lock()
	if s.imgAuto.running == nil {
		s.imgAuto.running = map[string]bool{}
	}
	if s.imgAuto.running[image] {
		s.imgAuto.mu.Unlock()
		return api.ImageUpgradeRun{}
	}
	s.imgAuto.running[image] = true
	s.imgAuto.mu.Unlock()
	defer func() {
		s.imgAuto.mu.Lock()
		delete(s.imgAuto.running, image)
		s.imgAuto.mu.Unlock()
	}()

	run := api.ImageUpgradeRun{StartedAt: s.now().UTC()}
	actx := WithActor(ctx, Actor{Name: "auto-upgrade", Kind: "system", Perm: api.PermAdmin, Global: true})
	fail := func(format string, a ...any) api.ImageUpgradeRun {
		run.Result, run.Detail = api.ImageUpgradeRunFailed, fmt.Sprintf(format, a...)
		return s.finishAutoRun(actx, image, run)
	}
	if _, err := s.imgKind.get(image); err != nil {
		return fail("the image is gone")
	}
	mgr, err := s.buildPool("")
	if err != nil {
		return fail("%v", err)
	}
	from := s.newestGeneration(actx, image)
	if from == 0 {
		return fail("the image has no generation yet; build one with 'jailops image build %s'", image)
	}
	resources := map[string][]string{"images": {"/api/v1/images/" + image}}
	var runErr error
	var mu sync.Mutex // the operation fills run while this function may stop waiting for it
	op, err := s.Submit(actx, OpSpec{
		Action: api.OpImageAutoUpgrade, Description: "Automatic upgrade of image " + image, Locks: []string{imageLock(image)}, Resources: resources,
		Run: func(ctx context.Context, h *OpHandle) (any, error) {
			res, err := s.upgradeImageRun(ctx, h, mgr, image, from, api.ImageUpgrade{})
			if err != nil {
				mu.Lock()
				runErr = err
				mu.Unlock()
				return nil, err
			}
			mu.Lock()
			if res.Generation != nil {
				run.Generation = res.Generation.Name
			}
			mu.Unlock()
			return res, nil
		}})
	if err != nil {
		return fail("%v", err)
	}
	if _, err := s.WaitOperation(actx, op.ID, 4*time.Hour); err != nil {
		return fail("%v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	switch {
	case runErr != nil:
		return fail("%v", runErr)
	case run.Generation == "":
		run.Result = api.ImageUpgradeRunUnchanged
	default:
		run.Result = api.ImageUpgradeRunOK
	}
	return s.finishAutoRun(actx, image, run)
}

func (s *Service) finishAutoRun(ctx context.Context, image string, run api.ImageUpgradeRun) api.ImageUpgradeRun {
	run.FinishedAt = s.now().UTC()
	s.appendImageRun(image, run)
	if run.Result == api.ImageUpgradeRunFailed {
		s.log.Warn("automatic image upgrade failed", "image", image, "detail", run.Detail)
		s.emit(ctx, "image-upgrade-failed", "/api/v1/images/"+image)
	}
	return run
}
