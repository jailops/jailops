// Package catalog is the file catalog of a directory stream: the entry model,
// its byte encoding, the scanner that walks a directory tree and compares it
// with the previous catalog, and extraction.
//
// A stream is two byte ranges in one dynamic chunk index:
//
//	[ payload: the bytes of every regular file, concatenated in walk order ]
//	[ catalog: the encoding of the metadata tree                          ]
//
// The catalog records for every entry its metadata, for files the position of
// their bytes in the payload and a SHA-256 of their content. It is chunked and
// encrypted like data, so the server never learns a file name. It serves four
// purposes: change detection at the next backup, browsing without downloading
// file data, single file and subtree extraction (only the chunks holding the
// requested bytes are fetched), and end-to-end verification of extracted
// content. The byte layout is specified in internal/repo/format/doc.go.
//
// A read-only mounted ZFS snapshot is just a directory to the scanner.
package catalog
