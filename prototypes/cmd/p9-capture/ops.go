package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"jailops.net/jailops/prototypes/internal/capture"
)

// applyOp performs one capture.Op against root, mirroring exactly the operation
// internal/capture.GenOps generated so the L0 walk and `zfs diff` are compared against the same
// filesystem history (design 9.4 step 4 / section 16 row 10).
func applyOp(root string, op capture.Op) error {
	p := filepath.Join(root, op.Path)
	np := ""
	if op.NewPath != "" {
		np = filepath.Join(root, op.NewPath)
	}
	switch op.Kind {
	case capture.OpCreate:
		return os.WriteFile(p, []byte("jop9 "+op.Path+"\n"), 0o644)
	case capture.OpCreateDir:
		return os.Mkdir(p, 0o755)
	case capture.OpModify:
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = f.WriteString("more\n")
		return err
	case capture.OpTruncate:
		return os.Truncate(p, 1)
	case capture.OpRename:
		return os.Rename(p, np)
	case capture.OpHardlink:
		return os.Link(p, np)
	case capture.OpDelete:
		return os.Remove(p)
	case capture.OpChmod:
		m, err := strconv.ParseUint(op.Arg, 8, 32)
		if err != nil {
			return err
		}
		return os.Chmod(p, os.FileMode(m))
	case capture.OpChown:
		uid, err := strconv.Atoi(op.Arg)
		if err != nil {
			return err
		}
		return os.Chown(p, uid, uid)
	case capture.OpXattr:
		name, val, ok := strings.Cut(op.Arg, "=")
		if !ok {
			return fmt.Errorf("bad xattr arg %q", op.Arg)
		}
		return exec.Command("setextattr", extattrNamespace, name, val, p).Run()
	case capture.OpAcl:
		return exec.Command("setfacl", "-m", op.Arg, p).Run()
	case capture.OpFlag:
		if _, ok := flagBitOf(op.Arg); !ok {
			return fmt.Errorf("unknown flag %q", op.Arg)
		}
		return exec.Command("chflags", op.Arg, p).Run()
	}
	return fmt.Errorf("unknown op kind %q", op.Kind)
}
