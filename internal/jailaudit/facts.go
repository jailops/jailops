// Package jailaudit is the audit of a FreeBSD jail (`jailops jail audit`): what the root helper reads from the jail's
// root on the host (Facts) and the checks made of it (Evaluate). The helper reads; nothing here writes, and nothing runs
// in the jail but the daemon's sockstat of a running one.
package jailaudit

// File is a file of a base package as pkg recorded it: the path in the jail and its checksum ("1$" and the SHA-256 of
// the content, as pkg 2 writes it).
type File struct {
	Path string `json:"path"`
	Sum  string `json:"sum,omitempty"`
}

// Facts is what the root helper read from a jail's root. It holds no password hash: the accounts are reduced to what
// the checks need.
type Facts struct {
	// Suspected are the findings of the helper's check before an upgrade (jail.check_root): a link out of the root on a
	// path pkg writes, a link among pkg's own files, a pkg.conf that turns plugins on.
	Suspected []string `json:"suspected,omitempty"`
	Accounts  Accounts `json:"accounts"`
	// SetID are the files with the set-user-ID or set-group-ID bit that are not files of a base package (the request's
	// Files), at most MaxSetID; SetIDMore counts the others. SetIDCut says the walk stopped at its bound (MaxWalk
	// entries). The walk stays on the root's file system: a volume is not walked.
	SetID     []string `json:"setid,omitempty"`
	SetIDMore int      `json:"setid_more,omitempty"`
	SetIDCut  bool     `json:"setid_cut,omitempty"`
	// Modified are the programs and libraries of the base whose content differs from what pkg recorded, at most
	// MaxListed; ModifiedMore counts the others, Verified the files compared. A file larger than MaxHashFile is
	// listed as modified without being read (no base program is that large). NotCompared counts the files
	// left out once the audit has read MaxHashTotal bytes.
	Modified     []string `json:"modified,omitempty"`
	ModifiedMore int      `json:"modified_more,omitempty"`
	Verified     int      `json:"verified,omitempty"`
	NotCompared  int      `json:"not_compared,omitempty"`
	// Mounts are the nullfs mounts below the root (the volumes and host directories of a running jail).
	Mounts []Mount `json:"mounts,omitempty"`
	// Texts are the configuration files the checks read (TextFiles and the repository files of pkg), path in the
	// jail to content; a file that is missing is absent. Unread names a file that exists and was not read, and why.
	Texts  map[string]string `json:"texts,omitempty"`
	Unread map[string]string `json:"unread,omitempty"`
	// SSHD says whether /usr/sbin/sshd is installed.
	SSHD bool `json:"sshd,omitempty"`
	// ShrcPrompt says whether /root/.shrc has the block that reads the JailOps prompt.
	ShrcPrompt bool `json:"shrc_prompt,omitempty"`
}

// Accounts is what the checks need of /etc/master.passwd.
type Accounts struct {
	// Read is false when the file is missing or cannot be read.
	Read bool `json:"read"`
	// UID0 are the accounts with uid 0 other than root and toor.
	UID0 []string `json:"uid0,omitempty"`
	// Root and Toor are PassLocked, PassSet, PassEmpty, or "" for an account that is not there.
	Root string `json:"root,omitempty"`
	Toor string `json:"toor,omitempty"`
	// Empty are the other accounts with an empty password field: they log in without a password.
	Empty []string `json:"empty,omitempty"`
	// RootShell is the shell of root.
	RootShell string `json:"root_shell,omitempty"`
}

// The password states of Accounts.Root and Accounts.Toor.
const (
	PassLocked = "locked"
	PassSet    = "set"
	PassEmpty  = "empty"
)

// Mount is a mount below a jail's root: the path in the jail, and whether it has nosuid.
type Mount struct {
	Path   string `json:"path"`
	NoSUID bool   `json:"nosuid"`
}

// Bounds of what the helper reads and returns.
const (
	MaxListed    = 20
	MaxSetID     = 1000
	MaxWalk      = 2_000_000
	MaxText      = 256 << 10
	MaxRepoFiles = 32
	// MaxTexts is what all of Texts may hold together, well below what one answer of the helper may be: a file
	// over it is not read (Unread), so a jail cannot make its audit fail by its size.
	MaxTexts = 1 << 20
	// MaxHashFile is the largest file compared with its checksum: no program or library of the base comes near it.
	MaxHashFile = 256 << 20
	// MaxHashTotal is what one audit reads to compare checksums; the files after it are counted in NotCompared.
	MaxHashTotal = 4 << 30
)

// ShrcBlock starts the block of /root/.shrc that reads the JailOps prompt.
const ShrcBlock = "# BEGIN jailops shell"

// TextFiles are the files the helper returns as text when they are plain files: nothing of them is a secret.
var TextFiles = []string{"/etc/rc.conf", "/etc/rc.conf.local", "/etc/periodic.conf", "/etc/periodic.conf.local", "/etc/hosts",
	"/etc/ssh/sshd_config"}

// RepoDirs are the directories of pkg's repository files, in the order pkg reads them (a later file overrides a key of
// an earlier one): the base's, then REPOS_DIR's default.
var RepoDirs = []string{"/etc/pkg", "/usr/local/etc/pkg/repos"}

// BaseBinDirs are the trees whose files are compared with the checksums of their base packages: the programs and
// libraries, never configuration a tenant edits.
var BaseBinDirs = []string{"/bin/", "/sbin/", "/lib/", "/libexec/", "/usr/bin/", "/usr/sbin/", "/usr/lib/", "/usr/lib32/", "/usr/libexec/"}
