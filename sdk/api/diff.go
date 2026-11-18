package api

// The kinds of a DiffEntry.
const (
	DiffAdded    = "added"
	DiffModified = "modified"
	DiffRemoved  = "removed"
	DiffRenamed  = "renamed"
)

// Where a DiffDataset was measured from.
const (
	// DiffBaseImage is the snapshot of the image generation the root of the jail is a clone of.
	DiffBaseImage = "image"
	// DiffBaseBaseline is the snapshot of the dataset taken when the jail was made.
	DiffBaseBaseline = "baseline"
	// DiffBaseTree is the same directory of the image, compared file by file: the jail has no baseline snapshot.
	DiffBaseTree = "image-tree"
)

// DefaultDiffLimit is how many entries of each dataset a diff lists when the request gives no limit.
const DefaultDiffLimit = 200

// MaxDiffLimit is the most entries of a dataset a diff lists: a reply stays well below the size a helper answer may have.
const MaxDiffLimit = 20000

// DiffEntry is one path of a jail that differs from its image. Path is a path inside the jail; To is the new path of a
// renamed entry.
type DiffEntry struct {
	Kind string `json:"kind"` // added, modified, removed or renamed
	Path string `json:"path"`
	To   string `json:"to,omitempty"`
}

// DiffDataset is what one dataset of a thin jail holds that the image does not: the root (the clone), or a persistent
// dataset (etc, local, var-log, home, ...).
type DiffDataset struct {
	Name string `json:"name"` // root, or a persistent dataset: etc, local, var-log, ...
	Path string `json:"path"` // where the dataset is mounted in the jail
	Base string `json:"base"` // image, baseline or image-tree
	Note string `json:"note,omitempty"`
	// Entries are at most Limit of the changes, in path order. Total and Counts cover all of them.
	Entries []DiffEntry    `json:"entries"`
	Total   int            `json:"total"`
	Counts  map[string]int `json:"counts"` // by kind
	// Truncated is true when Entries has fewer than Total entries.
	Truncated bool `json:"truncated,omitempty"`
}

// DiffPackages are the packages installed on purpose in the jail (the ones pkg does not mark as dependencies).
type DiffPackages struct {
	Packages []string `json:"packages"` // name-version
	Note     string   `json:"note,omitempty"`
}

// JailDiff is the reply of GET /api/v1/jails/{name}/diff: what a thin jail holds on top of its image.
type JailDiff struct {
	Jail  string `json:"jail"`
	Image string `json:"image"` // the image generation the root was cloned from, as name#gN
	// Datasets lists the root first, then the persistent datasets.
	Datasets []DiffDataset `json:"datasets"`
	// Packages is set when the request asked for them.
	Packages *DiffPackages `json:"packages,omitempty"`
}

// Total is the number of changes in all datasets.
func (d JailDiff) Total() int {
	n := 0
	for _, ds := range d.Datasets {
		n += ds.Total
	}
	return n
}
