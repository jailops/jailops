package datastore

import (
	"context"
	"testing"
)

func TestConformanceMem(t *testing.T) {
	RunConformance(t, func(t *testing.T) Backend { return NewMemBackend() })
}

func TestConformanceDir(t *testing.T) {
	RunConformance(t, func(t *testing.T) Backend {
		b, err := NewDirBackend(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return b
	})
}

func TestConformanceModeOnline(t *testing.T) {
	RunConformance(t, func(t *testing.T) Backend { return NewModeBackend(NewMemBackend(), ModeOnline) })
}

func TestConformanceFaultWrapperPassThrough(t *testing.T) {
	RunConformance(t, func(t *testing.T) Backend { return NewFaultBackend(NewMemBackend()) })
}

var _ = context.Background
