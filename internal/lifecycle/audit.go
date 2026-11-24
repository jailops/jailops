package lifecycle

import (
	"context"
	"fmt"
	"path"
	"strings"

	"jailops.net/jailops/internal/jailaudit"
	"jailops.net/jailops/sdk/api"
)

// RootAudit is what the audit of a jail read from its root: the helper's Facts, and from a copy of its package database
// the number of packages, the files they own and what pkg audit says of them.
type RootAudit struct {
	Facts jailaudit.Facts
	// HasPkgDB is false for a root without /var/db/pkg; Owned, Packages and BasePackages are then empty.
	HasPkgDB               bool
	Owned                  map[string]bool
	Packages, BasePackages int
	// VulnRan says pkg audit ran; Vulns are the vulnerable packages it named, VulnErr why it did not run.
	VulnRan bool
	Vulns   []jailaudit.VulnPackage
	VulnErr string
}

// rootAuditor is the part of Files that reads a jail's root for its audit (the helper's jail.audit_root).
type rootAuditor interface {
	AuditRoot(ctx context.Context, root string, files []jailaudit.File) (jailaudit.Facts, error)
}

// AuditRoot reads the root of a FreeBSD jail, running or stopped, for `jailops jail audit`. It changes nothing in the
// jail: a stopped jail's root is mounted for the read and unmounted after it (as BasePackages does), pkg reads a copy of
// the package database in a scratch directory of the container, and the helper reads the root. With vulns pkg audit
// reads the copy with the host's vulnerability database (jailaudit.HostVulnXML), which it never fetches. running says
// whether the jail runs.
func (s *Service) AuditRoot(ctx context.Context, name string, vulns bool) (RootAudit, bool, error) {
	ra, ok := s.Files.(rootAuditor)
	if !ok {
		return RootAudit{}, false, api.Errorf(api.CodeUnsupported, "this daemon cannot read the root of a jail (no root helper)")
	}
	st, err := s.ready(ctx, name)
	if err != nil {
		return RootAudit{}, false, err
	}
	if err := st.refuseUnsupported(); err != nil {
		return RootAudit{}, false, err
	}
	rec, err := s.readRecord(ctx, name)
	if err != nil {
		return RootAudit{}, false, fmt.Errorf("read spec.json: %w", err)
	}
	if rec.Spec.OS == api.OSLinux {
		return RootAudit{}, false, api.Errorf(api.CodeUnsupported, "the audit reads a FreeBSD jail; %s is a Linux jail", plainName(name))
	}
	running, err := s.Jails.Running(ctx, name)
	if err != nil {
		return RootAudit{}, false, err
	}
	if !running && s.prop(ctx, st.Root, "mounted") != "yes" {
		defer func() { _ = s.unmountTree(context.WithoutCancel(ctx), name) }()
		if err := s.mountTree(ctx, name); err != nil {
			return RootAudit{}, false, err
		}
	}
	root := s.Cfg.at(name).rootMP()
	out := RootAudit{Owned: map[string]bool{}}
	var files []jailaudit.File
	if ok, err := s.Files.Exists(ctx, path.Join(root, "var/db/pkg")); err != nil {
		return RootAudit{}, false, err
	} else if ok {
		work := fmt.Sprintf("%s/.upgrade-%s-audit%d", s.Cfg.at(name).containerMP(), s.stamp(""), checkSeq.Add(1))
		defer func() { _ = s.Files.RemoveAll(context.WithoutCancel(ctx), work) }()
		if err := s.copyPackageDB(ctx, st, work); err != nil {
			return RootAudit{}, false, err
		}
		// one line per file of every installed package, with pkg's checksum; a query reads the local database only
		q, err := s.Exec.Run(ctx, []string{"pkg", "-r", work + "/scratch", "query", "-a", "%n %Fp %Fs"})
		if err != nil {
			return RootAudit{}, false, fmt.Errorf("read the files of the packages of jail %s: %w: %s", plainName(name), err, tailLine(string(q)))
		}
		pkgs := map[string]bool{}
		for _, l := range strings.Split(string(q), "\n") {
			// NAME PATH SUM: the path may hold a space, the name and the checksum do not
			pkg, rest, ok := strings.Cut(l, " ")
			if !ok || !strings.HasPrefix(rest, "/") {
				continue
			}
			file, sum := rest, ""
			if i := strings.LastIndexByte(rest, ' '); i > 0 {
				file, sum = rest[:i], rest[i+1:]
			}
			pkgs[pkg] = true
			out.Owned[file] = true
			if !strings.HasPrefix(pkg, "FreeBSD-") {
				continue
			}
			bf := jailaudit.File{Path: file}
			if underBaseBin(file) {
				bf.Sum = sum
			}
			files = append(files, bf)
		}
		// a package that owns no file is counted too
		if n, err := s.Exec.Run(ctx, []string{"pkg", "-r", work + "/scratch", "query", "%n %v %sb"}); err == nil {
			for n := range parsePackages(string(n)) {
				pkgs[n] = true
			}
		}
		if vulns {
			// the packages of the copy against the host's database; pkg audit exits 1 when it names one
			v, err := s.Exec.Run(ctx, []string{"pkg", "-r", work + "/scratch", "audit", "-f", jailaudit.HostVulnXML})
			out.Vulns = jailaudit.ParsePkgAudit(string(v))
			if err != nil && len(out.Vulns) == 0 {
				msg := tailLine(string(v))
				if msg == "" {
					msg = err.Error()
				}
				out.VulnErr = "pkg audit: " + msg
			} else {
				out.VulnRan = true
			}
		}
		out.HasPkgDB = true
		for n := range pkgs {
			out.Packages++
			if strings.HasPrefix(n, "FreeBSD-") {
				out.BasePackages++
			}
		}
	}
	facts, err := ra.AuditRoot(ctx, root, files)
	if err != nil {
		return RootAudit{}, false, fmt.Errorf("read the root of jail %s: %w", plainName(name), err)
	}
	out.Facts = facts
	return out, running, nil
}

func underBaseBin(p string) bool {
	for _, d := range jailaudit.BaseBinDirs {
		if strings.HasPrefix(p, d) {
			return true
		}
	}
	return false
}
