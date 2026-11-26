package api

import (
	"strings"
	"time"
)

// Files of a jail: GET and PUT /api/v1/jails/{name}/files/{path...}.
//
// The path is the path inside the jail without the leading slash ("etc/motd"); the root of the jail is
// /api/v1/jails/{name}/files/. What the jail root holds is read and written from the host: a jail does not have to be
// started, but its root has to be mounted (the dataset of a stopped jail is not: the answer is busy).
//
//	GET  ...?stat=true            what is there: JailFileStat
//	GET  ...?list=true            the entries of a directory: JailFileList (extension jail_files_list)
//	GET  ...                      the bytes of a regular file
//	GET  ...?archive=tar          a tar stream of a file, a symlink or (recursive=true) a directory tree
//	PUT  ...                      writes the body (Content-Length required) as the regular file at the path
//	PUT  ...?archive=tar          writes the tar stream below the directory at the path
//
// Symbolic links are never followed, not in the path and not in a tree; devices, FIFOs and sockets are not copied; the
// set-user-id, set-group-id and sticky bits are never set. Owner and times are kept only with preserve=true.

// Query parameters of the file routes.
const (
	FileQueryStat      = "stat"
	FileQueryList      = "list"
	FileQueryArchive   = "archive"
	FileQueryRecursive = "recursive"
	FileQueryPreserve  = "preserve"
	FileQueryMkdir     = "mkdir"
	FileQueryMode      = "mode"
	FileQueryAs        = "as"
)

// FileArchiveTar is the value of the archive parameter.
const FileArchiveTar = "tar"

// MediaTar is the content type of a tar stream.
const MediaTar = "application/x-tar"

// HeaderFileMode carries the permission bits of a regular file that GET sends, in octal ("0644").
const HeaderFileMode = "X-Jailops-File-Mode"

// HeaderSkipped is a trailer of a tar GET: the number of devices, FIFOs and sockets left out.
const HeaderSkipped = "X-Jailops-Skipped"

// HeaderFileError is a trailer of a GET that failed after it had begun to send: the message of the error. A stream
// that ends without the trailing archive blocks, or with this trailer set, is incomplete.
const HeaderFileError = "X-Jailops-Error"

// MaxFilePathBytes bounds a path inside a jail, and MaxFilePathDepth the directories in it.
const (
	MaxFilePathBytes = 4096
	MaxFilePathDepth = 64
)

// JailFileType says what a path of a jail is.
type JailFileType string

// The types of JailFileStat.
const (
	FileTypeFile    JailFileType = "file"
	FileTypeDir     JailFileType = "dir"
	FileTypeSymlink JailFileType = "symlink"
	FileTypeOther   JailFileType = "other"
	FileTypeNone    JailFileType = "none" // nothing is there
)

// JailFileStat is what is at a path inside a jail. A missing path is not an error: its Type is "none".
type JailFileStat struct {
	Path       string       `json:"path"`
	Type       JailFileType `json:"type"`
	Size       int64        `json:"size_bytes,omitempty"`
	Mode       string       `json:"mode,omitempty"` // permission bits in octal, "0644"
	Uid        int          `json:"uid,omitempty"`  // the owner and group as numbers; absent is 0 (root, wheel)
	Gid        int          `json:"gid,omitempty"`
	ModifiedAt *time.Time   `json:"modified_at,omitempty"`
	Link       string       `json:"link,omitempty"` // the target of a symlink (text, never followed)
}

// MaxFileListEntries bounds the entries of one directory listing; a longer directory is cut and says so.
const MaxFileListEntries = 1000

// JailFileEntry is one entry of a directory of a jail: its name and what it is, without following a symlink.
type JailFileEntry struct {
	Name string       `json:"name"`
	Type JailFileType `json:"type"`
}

// JailFileList is the answer of ?list=true: the entries of the directory at Path, by name. Truncated says that the
// directory has more than MaxFileListEntries entries and some are not listed.
type JailFileList struct {
	Path      string          `json:"path"`
	Entries   []JailFileEntry `json:"entries"`
	Truncated bool            `json:"truncated,omitempty"`
}

// JailFileCopy is what a transfer did: the answer of PUT.
type JailFileCopy struct {
	Files    int   `json:"files"`
	Dirs     int   `json:"dirs"`
	Symlinks int   `json:"symlinks"`
	Skipped  int   `json:"skipped,omitempty"`
	Bytes    int64 `json:"bytes"`
}

// CleanJailFilePath checks a path inside a jail as the URL carries it (no leading slash) and returns it without a
// trailing slash. "" is the root of the jail. A path is plain: no empty, "." or ".." segment, no NUL byte, at most
// MaxFilePathBytes bytes and MaxFilePathDepth segments.
func CleanJailFilePath(p string) (string, error) {
	p = strings.TrimSuffix(strings.TrimPrefix(p, "/"), "/")
	if p == "" {
		return "", nil
	}
	if len(p) > MaxFilePathBytes || strings.ContainsRune(p, 0) {
		return "", Errorf(CodeInvalid, "the path is too long or has a NUL byte")
	}
	segs := strings.Split(p, "/")
	if len(segs) > MaxFilePathDepth {
		return "", Errorf(CodeInvalid, "the path is more than %d directories deep", MaxFilePathDepth)
	}
	for _, s := range segs {
		if s == "" || s == "." || s == ".." {
			return "", Errorf(CodeInvalid, "the path %q has an empty, . or .. segment", p).WithHint("write the path without // . or ..")
		}
	}
	return p, nil
}
