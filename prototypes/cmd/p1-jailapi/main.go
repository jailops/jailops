// Prototype 1: jail_set/jail_get/jail_remove from pure Go and LOCAL_PEERCRED over a unix socket.
//
// Written without access to FreeBSD. Everything marked UNVERIFIED: is what this run settles.
package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"jailops.net/jailops/prototypes/internal/harness"
	"jailops.net/jailops/prototypes/internal/jailapi"
)

const jailName = "jop1test"

func main() {
	if len(os.Args) == 3 && os.Args[1] == "__peercred-client" {
		c, err := net.Dial("unix", os.Args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		c.Write([]byte("x"))
		time.Sleep(500 * time.Millisecond)
		c.Close()
		return
	}
	harness.Exit(harness.Spec{
		N:       1,
		Name:    "jailapi",
		Summary: "jail_set/jail_get/jail_remove via x/sys SYS_JAIL_* and LOCAL_PEERCRED, compared with jls",
		Plan: []string{
			"create dataset <pool>/jailops-proto-1/jail and use its mountpoint as the jail path",
			"print the x/sys constants the code depends on",
			"jail_set(JAIL_CREATE) a persistent jail named " + jailName + " with host.hostname",
			"jail_get it back by name and by jid, compare every field with `jls -n`",
			"jail_set(JAIL_UPDATE) host.hostname and securelevel, read back",
			"enumerate jails with lastjid and compare the set with `jls`",
			"jail_remove it, confirm it is gone from jail_get and jls",
			"LOCAL_PEERCRED: accept a unix connection as root and as user nobody and read the uid",
			"cleanup: remove jails named jop1*, destroy the dataset tree",
		},
		Run:     run,
		Cleanup: cleanup,
	})
}

func removeOwned(e *harness.Env) {
	for _, j := range jailapi.ParseJLSn(e.Quiet("jls", "-d", "-n", "jid", "name").Out()) {
		if !jailapi.OwnedName(j["name"]) {
			continue
		}
		jid, _ := strconv.Atoi(j["jid"])
		if err := jailapi.Remove(jid); err != nil {
			e.Note("remove %s: %v", j["name"], err)
		}
	}
}

func cleanup(e *harness.Env) {
	removeOwned(e)
	if e.DatasetExists(e.Root) {
		e.Zfs("destroy", "-r", e.Root)
	}
}

func jlsFields(e *harness.Env, jid int) harness.Result {
	return e.Quiet("jls", "-n", "-j", strconv.Itoa(jid), "jid", "name", "path", "host.hostname", "securelevel")
}

func getByName(name string) (jid int, ps []jailapi.Param, err error) {
	ps = []jailapi.Param{
		jailapi.Str("name", name),
		jailapi.Buf("jid", 4), jailapi.Buf("path", 1024), jailapi.Buf("host.hostname", 256),
		jailapi.Buf("securelevel", 4), jailapi.Buf("persist", 4), jailapi.ErrBuf(),
	}
	jid, err = jailapi.Get(ps, 0)
	return
}

func run(e *harness.Env) {
	e.Require("jls", "jexec", "zfs")
	e.Step("1. x/sys constants")
	cs := jailapi.Consts()
	keys := make([]string, 0, len(cs))
	for k := range cs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&sb, "%s=%d ", k, cs[k])
	}
	e.Check("x/sys constants present", len(cs) >= 6, strings.TrimSpace(sb.String()))
	e.Resolve("SYS_JAIL_SET/SYS_JAIL_GET/SYS_JAIL_REMOVE exist in golang.org/x/sys/unix for freebsd (they compiled)")
	e.Note("JAIL_CREATE/UPDATE/ATTACH/DYING are defined locally (not in x/sys); see UNVERIFIED in params.go")

	e.Step("2. scratch dataset")
	e.OnCleanup(func() { removeOwned(e) })
	e.Must("create dataset", e.Zfs("create", "-p", e.Root+"/jail").OK(), "zfs create")
	path := e.ZfsGet("mountpoint", e.Root+"/jail")
	e.Note("jail path: %s", path)
	if e.DryRun {
		path = "/dry/run"
	}

	e.Step("3. jail_set CREATE persistent")
	create := []jailapi.Param{jailapi.Str("name", jailName), jailapi.Str("path", path),
		jailapi.Str("host.hostname", "jop1.example"), jailapi.Flag("persist"), jailapi.ErrBuf()}
	jid := 0
	if !e.DryRun {
		var err error
		jid, err = jailapi.Set(create, jailapi.JailCreate)
		persistMode := "zero-length flag value"
		if err != nil {
			e.Note("flag form failed: %v; retrying with persist as int32 1", err)
			create = []jailapi.Param{jailapi.Str("name", jailName), jailapi.Str("path", path),
				jailapi.Str("host.hostname", "jop1.example"), jailapi.Int32("persist", 1), jailapi.ErrBuf()}
			jid, err = jailapi.Set(create, jailapi.JailCreate)
			persistMode = "int32 1 value"
		}
		e.Must("jail_set create returns jid", err == nil && jid > 0, fmt.Sprintf("jid=%d err=%v persist encoded as %s", jid, err, persistMode))
		e.Resolve("jail_set via unix.Syscall(SYS_JAIL_SET) with iovec pairs creates a jail; persist encoding: " + persistMode)
	} else {
		e.Note("DRY-RUN would call jail_set(JAIL_CREATE) with name,path,host.hostname,persist")
	}

	e.Step("4. jail_get by name, compare with jls")
	if !e.DryRun {
		gj, ps, err := getByName(jailName)
		e.Must("jail_get by name", err == nil && gj == jid, fmt.Sprintf("returned jid=%d want %d err=%v", gj, jid, err))
		gotJid, _ := jailapi.AsInt32(mustFind(ps, "jid"))
		hn := jailapi.CString(mustFind(ps, "host.hostname"))
		gp := jailapi.CString(mustFind(ps, "path"))
		pv, _ := jailapi.AsInt32(mustFind(ps, "persist"))
		e.Check("jail_get fields", int(gotJid) == jid && hn == "jop1.example" && gp == path && pv != 0,
			fmt.Sprintf("jid=%d hostname=%q path=%q persist=%d", gotJid, hn, gp, pv))
		r := jlsFields(e, jid)
		js := jailapi.ParseJLSn(r.Out())
		ok := len(js) == 1 && js[0]["name"] == jailName && js[0]["host.hostname"] == "jop1.example" && js[0]["path"] == path
		e.Check("jls agrees with jail_get", ok, r.Out())
	}

	e.Step("5. jail_set UPDATE and read back")
	if !e.DryRun {
		up := []jailapi.Param{jailapi.Str("name", jailName), jailapi.Str("host.hostname", "jop1-new.example"),
			jailapi.Int32("securelevel", 2), jailapi.ErrBuf()}
		ujid, err := jailapi.Set(up, jailapi.JailUpdate)
		e.Check("jail_set update", err == nil && ujid == jid, fmt.Sprintf("jid=%d err=%v", ujid, err))
		_, ps, err := getByName(jailName)
		hn := jailapi.CString(mustFind(ps, "host.hostname"))
		sl, _ := jailapi.AsInt32(mustFind(ps, "securelevel"))
		e.Check("update visible via jail_get", err == nil && hn == "jop1-new.example" && sl == 2, fmt.Sprintf("hostname=%q securelevel=%d err=%v", hn, sl, err))
		js := jailapi.ParseJLSn(jlsFields(e, jid).Out())
		e.Check("update visible via jls", len(js) == 1 && js[0]["host.hostname"] == "jop1-new.example" && js[0]["securelevel"] == "2", fmt.Sprint(js))
		// A securelevel can never be lowered: expected failure shows errors surface with errmsg.
		bad := []jailapi.Param{jailapi.Str("name", jailName), jailapi.Int32("securelevel", 0), jailapi.ErrBuf()}
		_, err = jailapi.Set(bad, jailapi.JailUpdate)
		e.Check("error path returns an error (securelevel lower refused)", err != nil, fmt.Sprint(err))
	}

	e.Step("6. enumerate with lastjid, compare with jls")
	if !e.DryRun {
		var ours []string
		last := int32(0)
		for i := 0; i < 4096; i++ {
			ps := []jailapi.Param{jailapi.Int32("lastjid", last), jailapi.Buf("jid", 4), jailapi.Buf("name", 256), jailapi.ErrBuf()}
			j, err := jailapi.Get(ps, 0)
			if err != nil {
				if !errors.Is(err, syscall.ENOENT) {
					e.Check("enumerate ends with ENOENT", false, err.Error())
				}
				break
			}
			last = int32(j)
			ours = append(ours, fmt.Sprintf("%d:%s", j, jailapi.CString(mustFind(ps, "name"))))
		}
		var theirs []string
		for _, j := range jailapi.ParseJLSn(e.Quiet("jls", "-n", "jid", "name").Out()) {
			theirs = append(theirs, j["jid"]+":"+j["name"])
		}
		e.Check("jail_get(lastjid) set equals jls set", strings.Join(ours, ",") == strings.Join(theirs, ","),
			"go="+strings.Join(ours, ",")+" jls="+strings.Join(theirs, ","))
	}

	e.Step("7. jail_remove")
	if !e.DryRun {
		_, ps, err := getByName(jailName)
		name := jailapi.CString(mustFind(ps, "name"))
		if name == "" {
			name = jailName
		}
		e.Must("ownership check before remove", err == nil && jailapi.OwnedName(name), name)
		err = jailapi.Remove(jid)
		e.Check("jail_remove", err == nil, fmt.Sprint(err))
		_, _, err = getByName(jailName)
		e.Check("jail_get after remove is ENOENT", errors.Is(err, syscall.ENOENT), fmt.Sprint(err))
		out := e.Quiet("jls", "-n", "jid", "name").Out()
		e.Check("jls no longer lists it", !strings.Contains(out, jailName), out)
		// A persistent jail removed with processes absent should not linger as dying.
		dy := e.Quiet("jls", "-d", "-n", "jid", "name").Out()
		e.Note("jls -d (includes dying): %s", strings.ReplaceAll(dy, "\n", " | "))
	} else {
		e.Note("DRY-RUN would jail_get, compare with jls, jail_set UPDATE, enumerate, jail_remove")
	}

	e.Step("8. LOCAL_PEERCRED")
	peercred(e)
	e.Resolve("LOCAL_PEERCRED via x/sys GetsockoptXucred works from pure Go on FreeBSD (see checks)")
}

func mustFind(ps []jailapi.Param, n string) []byte {
	p, _ := jailapi.Find(ps, n)
	return p.Val
}

func peercred(e *harness.Env) {
	if e.DryRun {
		e.Note("DRY-RUN would listen on a unix socket in a temp dir, connect as root and as nobody, read LOCAL_PEERCRED")
		return
	}
	dir, err := os.MkdirTemp("", "jop1-")
	e.Must("temp dir", err == nil, fmt.Sprint(err))
	defer os.RemoveAll(dir)
	os.Chmod(dir, 0o755)
	sock := filepath.Join(dir, "s")
	l, err := net.Listen("unix", sock)
	e.Must("listen", err == nil, fmt.Sprint(err))
	defer l.Close()
	os.Chmod(sock, 0o777)
	exe, _ := os.Executable()
	// Copy the binary where nobody can execute it (the build dir may be private).
	cp := filepath.Join(dir, "client")
	data, _ := os.ReadFile(exe)
	os.WriteFile(cp, data, 0o755)

	try := func(label string, uid uint32, cred bool) {
		cmd := exec.Command(cp, "__peercred-client", sock)
		if cred {
			cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: uid}}
		}
		if err := cmd.Start(); err != nil {
			e.Check("peercred "+label, false, "start client: "+err.Error())
			return
		}
		defer cmd.Wait()
		l.(*net.UnixListener).SetDeadline(time.Now().Add(5 * time.Second))
		c, err := l.Accept()
		if err != nil {
			e.Check("peercred "+label, false, "accept: "+err.Error())
			return
		}
		defer c.Close()
		pc, err := jailapi.PeerCred(c.(*net.UnixConn))
		e.Check("peercred "+label, err == nil && pc.Uid == uid, fmt.Sprintf("uid=%d groups=%v err=%v want uid=%d", pc.Uid, pc.Groups, err, uid))
	}
	try("as root", 0, false)
	// UNVERIFIED: user nobody has uid 65534 on FreeBSD.
	try("as nobody", 65534, true)
}
