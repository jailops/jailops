package chunker

import "hash/fnv"

// Packing of small files.
//
// Files smaller than SmallFile are not chunked on their own: consecutive small
// files, in walk order, are concatenated into one pack, and a pack is one
// chunk. A pack ends when it reaches PackMax bytes, or, once it holds at least
// PackMin bytes, after a file whose path has FNV-1a-32(path) mod PackDiv == 0.
// Pack boundaries therefore depend only on names and sizes: adding or removing
// a file disturbs the packs up to the next name-defined boundary, not every
// pack after it.
const (
	DefaultSmallFile = 256 << 10
	DefaultPackMin   = 512 << 10
	DefaultPackMax   = 2 << 20
	DefaultPackDiv   = 8
)

// PackPolicy decides how files are grouped into runs. The zero value is
// completed by Norm with the format defaults.
type PackPolicy struct {
	SmallFile int64
	PackMin   int64
	PackMax   int64
	PackDiv   uint32
}

// Norm returns the policy with zero fields replaced by the defaults.
func (p PackPolicy) Norm() PackPolicy {
	if p.SmallFile <= 0 {
		p.SmallFile = DefaultSmallFile
	}
	if p.PackMin <= 0 {
		p.PackMin = DefaultPackMin
	}
	if p.PackMax <= 0 {
		p.PackMax = DefaultPackMax
	}
	if p.PackDiv == 0 {
		p.PackDiv = DefaultPackDiv
	}
	return p
}

// Large reports whether a file of this size is chunked on its own.
func (p PackPolicy) Large(size int64) bool { return size >= p.SmallFile }

// EndsPack reports whether a pack that now holds cum bytes, the last file
// added having the path rel, ends after that file.
func (p PackPolicy) EndsPack(cum int64, rel string) bool {
	return cum >= p.PackMax || (cum >= p.PackMin && NameHash(rel)%p.PackDiv == 0)
}

// NameHash is the FNV-1a 32-bit hash of the path (slash separated, relative to
// the stream root, no leading slash).
func NameHash(rel string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(rel))
	return h.Sum32()
}
