package service

import (
	"testing"
	"time"
)

func TestExecLimitsDefaults(t *testing.T) {
	for _, c := range []struct {
		in, want ExecLimits
	}{
		{ExecLimits{}, ExecLimits{PerJail: 8}},
		{ExecLimits{PerJail: 2, Idle: time.Minute, Max: time.Hour}, ExecLimits{PerJail: 2, Idle: time.Minute, Max: time.Hour}},
		{ExecLimits{Idle: -time.Second, Max: -time.Second}, ExecLimits{PerJail: 8}},
	} {
		if got := c.in.withDefaults(); got != c.want {
			t.Errorf("%+v: %+v, want %+v", c.in, got, c.want)
		}
	}
}
