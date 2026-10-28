package zfsadapt

import (
	"context"
	"testing"
	"time"

	"jailops.net/jailops/sdk/api"
)

func TestABusyUnmountIsTriedAgain(t *testing.T) {
	unmountWait = time.Millisecond
	calls := 0
	a := &Adapter{UnmountFn: func(context.Context, string) error {
		calls++
		if calls < 4 {
			return api.Errorf(api.CodeBusy, "pool or dataset is busy")
		}
		return nil
	}}
	if err := a.Unmount(context.Background(), "tank/x"); err != nil || calls != 4 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	// other errors are not retried
	calls = 0
	a.UnmountFn = func(context.Context, string) error { calls++; return api.Errorf(api.CodeInvalid, "no") }
	if err := a.Unmount(context.Background(), "tank/x"); err == nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	// busy for good: the error comes back after the tries
	defer func(n int) { unmountTries = n }(unmountTries)
	unmountTries, calls = 3, 0
	a.UnmountFn = func(context.Context, string) error { calls++; return api.Errorf(api.CodeBusy, "busy") }
	if err := a.Unmount(context.Background(), "tank/x"); !api.IsCode(err, api.CodeBusy) || calls != 3 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
