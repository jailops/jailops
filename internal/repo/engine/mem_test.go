package engine

import (
	"runtime"
	"sync/atomic"
	"time"
)

// measurePeak runs fn and returns how far the heap in use grew above its level at
// the start, sampled every few milliseconds.
func measurePeak(fn func()) uint64 {
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	base := ms.HeapInuse
	var peak atomic.Uint64
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		var m runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
			}
			runtime.ReadMemStats(&m)
			if m.HeapInuse > base && m.HeapInuse-base > peak.Load() {
				peak.Store(m.HeapInuse - base)
			}
		}
	}()
	fn()
	close(stop)
	<-done
	return peak.Load()
}
