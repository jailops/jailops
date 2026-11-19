package service

import (
	"context"
	"errors"
	"io"

	"jailops.net/jailops/sdk/api"
	"jailops.net/jailops/sdk/tarcopy"
)

// JailFiles reads and writes the files below a jail root, from the host: the privileged helper in production
// (internal/priv/jailfiles.go), a directory in tests. Every method names the jail (<project>_<name>) and its root
// path; a path below the root is relative ("" is the root). A root that is not mounted answers not_found with the
// detail missing=root. Symbolic links are never followed.
type JailFiles interface {
	// JailStat says what is at a path (a missing one has the type "none").
	JailStat(ctx context.Context, q JailStatReq) (api.JailFileStat, error)
	// JailGet writes the file, symlink or tree at the path to out as a tar stream, its entries named below As.
	JailGet(ctx context.Context, q JailGetReq, out io.Writer) (tarcopy.Stats, error)
	// JailPut writes the tar stream in below the directory at the path.
	JailPut(ctx context.Context, q JailPutReq, in io.Reader) (tarcopy.Stats, error)
}

// JailStatReq names a path of a jail.
type JailStatReq struct {
	Jail string `json:"jail"`
	Root string `json:"root"`
	Path string `json:"path,omitempty"`
}

// JailGetReq asks for the tar stream of a path. Path is never the root of the jail.
type JailGetReq struct {
	Jail      string `json:"jail"`
	Root      string `json:"root"`
	Path      string `json:"path"`
	As        string `json:"as,omitempty"`
	Recursive bool   `json:"recursive,omitempty"`
	Preserve  bool   `json:"preserve,omitempty"`
	MaxBytes  int64  `json:"max_bytes,omitempty"`
}

// JailPutReq writes a tar stream below the directory Path ("" is the root of the jail); Mkdir creates the
// directories of Path that are missing.
type JailPutReq struct {
	Jail     string `json:"jail"`
	Root     string `json:"root"`
	Path     string `json:"path,omitempty"`
	Mkdir    bool   `json:"mkdir,omitempty"`
	Preserve bool   `json:"preserve,omitempty"`
	MaxBytes int64  `json:"max_bytes,omitempty"`
}

// DefaultFileCopyMax is the most bytes one file transfer carries unless the daemon is configured otherwise.
const DefaultFileCopyMax int64 = 2 << 30

// FileCopyMaxBytes is the byte limit of one transfer.
func (s *Service) FileCopyMaxBytes() int64 {
	if s.d.FileCopyMax > 0 {
		return s.d.FileCopyMax
	}
	return DefaultFileCopyMax
}

// JailFileGetOpts are the options of a transfer out of a jail.
type JailFileGetOpts struct {
	Recursive bool
	Preserve  bool
	// As names the entries of the archive below this name (default: the last element of the path).
	As string
}

// JailFilePutOpts are the options of a transfer into a jail.
type JailFilePutOpts struct {
	Mkdir    bool
	Preserve bool
}

// fileJail resolves the jail of a file request: the project rules of every jail route, a jail that exists, and a
// daemon that has the helper.
func (s *Service) fileJail(ctx context.Context, name string) (p project, key string, err error) {
	if s.d.JailFiles == nil {
		return p, "", api.Errorf(api.CodeUnsupported, "this daemon has no way to read or write files of jails").
			WithHint("jailops-priv offers it; run jailopsd with the helper")
	}
	p, key, err = s.jailRef(ctx, name)
	if err != nil {
		return p, "", err
	}
	if _, err = s.getByKey(ctx, key); err != nil {
		return p, "", err
	}
	return p, key, nil
}

// fileErr maps what the helper answered: a root that is not mounted is a jail that is not running (its root dataset is
// mounted while it runs), a missing path is not_found in the jail's terms.
func (s *Service) fileErr(err error, name, key, p string) error {
	var ae *api.Error
	if !errors.As(err, &ae) {
		return internal(err)
	}
	switch {
	case ae.Code == api.CodeNotFound && ae.Detail["missing"] == "root":
		return api.Errorf(api.CodeBusy, "the files of jail %q cannot be reached: its root is not mounted", name).
			WithHint("start the jail, or mount its root dataset on the host: zfs mount " + s.lcfg.RootDS(key))
	case ae.Code == api.CodeNotFound:
		return api.Errorf(api.CodeNotFound, "jail %q has no /%s", name, p).WithHint("create the directory first, or write with mkdir=true; ?stat=true says what exists")
	case ae.Code == api.CodeForbidden || ae.Code == api.CodeInvalid || ae.Code == api.CodeBusy:
		return err
	}
	return internal(err)
}

// JailFileStat says what is at a path of a jail root ("" is the root).
func (s *Service) JailFileStat(ctx context.Context, name, p string) (api.JailFileStat, error) {
	p, err := api.CleanJailFilePath(p)
	if err != nil {
		return api.JailFileStat{}, err
	}
	_, key, err := s.fileJail(ctx, name)
	if err != nil {
		return api.JailFileStat{}, err
	}
	st, err := s.d.JailFiles.JailStat(ctx, JailStatReq{Jail: key, Root: s.rootMount(key), Path: p})
	if err != nil {
		return api.JailFileStat{}, s.fileErr(err, name, key, p)
	}
	return st, nil
}

// JailFileGet writes the tar stream of a path of a jail root to out. The project of the jail is returned for the audit
// entry the caller writes.
func (s *Service) JailFileGet(ctx context.Context, name, p string, o JailFileGetOpts, out io.Writer) (string, tarcopy.Stats, error) {
	p, err := api.CleanJailFilePath(p)
	if err != nil {
		return "", tarcopy.Stats{}, err
	}
	pr, key, err := s.fileJail(ctx, name)
	if err != nil {
		return "", tarcopy.Stats{}, err
	}
	st, err := s.d.JailFiles.JailGet(ctx, JailGetReq{Jail: key, Root: s.rootMount(key), Path: p, As: o.As,
		Recursive: o.Recursive, Preserve: o.Preserve, MaxBytes: s.FileCopyMaxBytes()}, out)
	if err != nil {
		return pr.Meta.Name, st, s.fileErr(err, name, key, p)
	}
	return pr.Meta.Name, st, nil
}

// JailFilePut writes the tar stream in below the directory p of a jail root.
func (s *Service) JailFilePut(ctx context.Context, name, p string, o JailFilePutOpts, in io.Reader) (string, tarcopy.Stats, error) {
	p, err := api.CleanJailFilePath(p)
	if err != nil {
		return "", tarcopy.Stats{}, err
	}
	pr, key, err := s.fileJail(ctx, name)
	if err != nil {
		return "", tarcopy.Stats{}, err
	}
	st, err := s.d.JailFiles.JailPut(ctx, JailPutReq{Jail: key, Root: s.rootMount(key), Path: p, Mkdir: o.Mkdir,
		Preserve: o.Preserve, MaxBytes: s.FileCopyMaxBytes()}, in)
	if err != nil {
		return pr.Meta.Name, st, s.fileErr(err, name, key, p)
	}
	return pr.Meta.Name, st, nil
}
