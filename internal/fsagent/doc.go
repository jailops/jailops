// Package fsagent lets an unprivileged process read a tree and write a tree it has no permission for,
// through a small process that has the permission and does exactly two jobs.
//
// A backup reads every file of a ZFS snapshot (some are readable by root only), and a restore writes files
// of any owner. jailopsd does not run as root, so jailops-priv starts an agent for it: a copy of the helper
// binary that changes its root directory (chroot) to one directory the helper has checked, and then speaks
// the protocol below on a socket pair whose other end the daemon holds. A read agent answers three
// questions (stat an entry, list a directory, open a file) and a write agent applies the operations of
// catalog.Sink. Neither can reach anything outside its directory, and a read agent writes nothing.
//
// The client side implements catalog.ScanFS (read) and catalog.Sink (write), so the backup engine and the
// extraction run unchanged on top of it.
//
// # Protocol
//
// Frames: a 4 byte big endian length of what follows, a kind byte, a 4 byte request id and the body
// (length - 5 bytes, at most MaxFrame). Kinds:
//
//	Q  request   body: JSON {"op": ..., ...}
//	R  reply     body: JSON result
//	X  error     body: JSON {"msg", "errno", "socket"}
//	D  data      body: raw bytes of a file (either direction)
//	E  end       the data of a stream is complete
//	A  abort     the sender abandons a stream
//	C  credit    body: 4 byte count of bytes the reader of a stream has consumed (read side flow control)
//
// A read agent serves requests concurrently; a file is streamed in D frames within the credit the client
// gave. A write agent serves one request at a time; "write" is followed by D frames and E.
package fsagent
