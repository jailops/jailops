package main

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"jailops.net/jailops/prototypes/internal/harness"
)

// write creates a file with content, refusing to write outside mountBase (the only tree this
// prototype may touch).
func write(e *harness.Env, path string, content []byte) {
	if e.DryRun {
		e.Note("would write %s (%d bytes)", path, len(content))
		return
	}
	if !strings.HasPrefix(path, mountBase+"/") {
		e.Abort("refusing to write outside %s: %s", mountBase, path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		e.Abort("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		e.Abort("write %s: %v", path, err)
	}
}

// populateLive builds the mixed tree design 9.4 (section 16 row 11) names: many small files, a
// few large ones, a sparse file, hard links, symlinks, a long name, extended attributes, NFSv4
// ACLs and file flags. It returns the "hot" subset the writer goroutine will keep modifying.
func populateLive(e *harness.Env, dir string) []string {
	e.Step("populate %s", dir)
	var hot []string

	// Many small files.
	for i := 0; i < 60; i++ {
		p := fmt.Sprintf("small/f%03d", i)
		write(e, filepath.Join(dir, p), []byte(fmt.Sprintf("small file %d\n", i)))
	}
	// A few large files (uncompressible, so the snapshot/send paths actually move bytes).
	for i := 0; i < 2; i++ {
		p := fmt.Sprintf("large/f%d", i)
		full := filepath.Join(dir, p)
		if e.DryRun {
			e.Note("would write %s (8 MiB random)", full)
			continue
		}
		os.MkdirAll(filepath.Dir(full), 0o755)
		f, err := os.Create(full)
		if err != nil {
			e.Abort("%v", err)
		}
		r := rand.New(rand.NewSource(int64(i) + 1))
		buf := make([]byte, 1<<20)
		for n := 0; n < 8; n++ {
			r.Read(buf)
			f.Write(buf)
		}
		f.Close()
	}
	// A sparse file: a hole in the middle.
	if !e.DryRun {
		full := filepath.Join(dir, "sparse/f0")
		os.MkdirAll(filepath.Dir(full), 0o755)
		f, err := os.Create(full)
		if err != nil {
			e.Abort("%v", err)
		}
		f.WriteString("start\n")
		f.Truncate(16 << 20) // 16 MiB hole
		f.WriteAt([]byte("end\n"), 16<<20)
		f.Close()
	} else {
		e.Note("would write sparse/f0 with a 16 MiB hole")
	}
	// Hard links: three names, one inode.
	write(e, filepath.Join(dir, "links/primary"), []byte("linked content\n"))
	if !e.DryRun {
		os.Link(filepath.Join(dir, "links/primary"), filepath.Join(dir, "links/second"))
		os.Link(filepath.Join(dir, "links/primary"), filepath.Join(dir, "links/third"))
	}
	// Symlinks: relative and absolute-looking (but inside the tree).
	if !e.DryRun {
		os.Symlink("../small/f000", filepath.Join(dir, "linkrel"))
		os.Symlink(filepath.Join(dir, "small/f001"), filepath.Join(dir, "linkabs"))
	}
	// A long name (close to NAME_MAX=255).
	write(e, filepath.Join(dir, strings.Repeat("l", 200)), []byte("long name file\n"))

	// Extended attributes, ACLs and flags on a few "hot" files that the writer will keep
	// touching, plus a few "cold" files that get the same metadata once and are never touched
	// again (the fields test 1 compares against the snapshot view).
	for i := 0; i < 5; i++ {
		p := fmt.Sprintf("hot/f%d", i)
		write(e, filepath.Join(dir, p), []byte("hot file\n"))
		hot = append(hot, p)
	}
	for i := 0; i < 5; i++ {
		p := fmt.Sprintf("cold/f%d", i)
		full := filepath.Join(dir, p)
		write(e, full, []byte("cold file\n"))
		e.Run("setextattr", extattrNamespace, "jop9", fmt.Sprintf("value%d", i), full)
		e.Run("setfacl", "-m", fmt.Sprintf("user:%d:rwx:allow", 3000+i), full)
	}
	// Flags: nodump and uappnd on cold files, schg on one (and lifted again so the writer and
	// later cleanup can still touch its directory entries if needed; schg's own effect is the
	// claim being tested, see test 1's detail).
	e.Run("chflags", "nodump", filepath.Join(dir, "cold/f0"))
	e.Run("chflags", "uappnd", filepath.Join(dir, "cold/f1"))
	e.Run("chflags", "schg", filepath.Join(dir, "cold/f2"))
	e.OnCleanup(func() { e.Run("chflags", "noschg", filepath.Join(dir, "cold/f2")) })

	e.Note("populated: %d small, 2 large, 1 sparse, 3 hardlinked names, 2 symlinks, 1 long name, 5 hot, 5 cold (xattr+acl+flags)", 60)
	return hot
}

// writerQ is the background writer that keeps modifying the "hot" files while snapshots are
// taken, so test 1 captures a crash-consistent snapshot rather than a quiesced one.
type writerQ struct {
	stopCh chan struct{}
	wg     sync.WaitGroup
	writes int64
}

func startWriter(e *harness.Env, dir string, hot []string) *writerQ {
	w := &writerQ{stopCh: make(chan struct{})}
	if e.DryRun || len(hot) == 0 {
		return w
	}
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		r := rand.New(rand.NewSource(1))
		t := time.NewTicker(time.Duration(writerEvery) * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-w.stopCh:
				return
			case <-t.C:
			}
			p := filepath.Join(dir, hot[r.Intn(len(hot))])
			f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0)
			if err == nil {
				fmt.Fprintf(f, "write %d at %s\n", atomic.AddInt64(&w.writes, 1), time.Now().Format(time.RFC3339Nano))
				f.Close()
			}
		}
	}()
	return w
}

func (w *writerQ) stop() {
	select {
	case <-w.stopCh:
	default:
		close(w.stopCh)
	}
	w.wg.Wait()
}

func (w *writerQ) count() int64 { return atomic.LoadInt64(&w.writes) }
