package main

import (
	"context"
	"os/exec"
	"time"
)

// sampleExec runs a read-only command silently for the sampler goroutine.
func sampleExec(argv []string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
	return string(out), err
}
