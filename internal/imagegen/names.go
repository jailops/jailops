package imagegen

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"

	"jailops.net/jailops/sdk/api"
)

// Property names. The lifecycle package reads some of them from generation
// datasets and keeps its own copies of the strings.
const (
	PropManaged   = "jailops:managed"
	PropKind      = "jailops:kind"
	PropImage     = "jailops:image"
	PropGen       = "jailops:gen"
	PropParent    = "jailops:parent"
	PropManifest  = "jailops:manifest"
	PropPackages  = "jailops:packages"
	PropOSRelease = "jailops:osrelease"
	PropCreated   = "jailops:created"
	PropSource    = "jailops:source"
	PropOS        = "jailops:os"
	PropDeleted   = "jailops:deleted"
	PropAliases   = "jailops:aliases"
	// PropLastGen records the highest generation number ever built, so numbers
	// are never reused after generations are deleted.
	PropLastGen = "jailops:last_gen"

	KindGeneration = "generation"
	KindStaging    = "generation-staging"
	KindImage      = "image"

	// HoldTag protects every @ro snapshot against accidental destruction.
	HoldTag = "jailops-ro"
	// ROSnap is the name of the immutable snapshot.
	ROSnap = "ro"
)

var (
	imageRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	aliasRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)
	genRe   = regexp.MustCompile(`^g([0-9]+)$`)
)

// GenerationName is "<image>#g<N>".
func GenerationName(image string, n int) string { return fmt.Sprintf("%s#g%d", image, n) }

// Ref is a parsed image reference: "<image>@<alias>", "<image>#g<N>" or a
// bare "<image>" (the newest generation).
type Ref struct {
	Image string
	Alias string
	Gen   int
}

// ParseRef parses an image reference.
func ParseRef(s string) (Ref, error) {
	if img, a, ok := strings.Cut(s, "@"); ok {
		if !imageRe.MatchString(img) || !aliasRe.MatchString(a) {
			return Ref{}, api.Errorf(api.CodeInvalid, "bad image reference %q", s)
		}
		return Ref{Image: img, Alias: a}, nil
	}
	if img, g, ok := strings.Cut(s, "#"); ok {
		m := genRe.FindStringSubmatch(g)
		if !imageRe.MatchString(img) || m == nil {
			return Ref{}, api.Errorf(api.CodeInvalid, "bad image reference %q", s)
		}
		n, err := strconv.Atoi(m[1])
		if err != nil || n < 1 {
			return Ref{}, api.Errorf(api.CodeInvalid, "bad generation in %q", s)
		}
		return Ref{Image: img, Gen: n}, nil
	}
	if !imageRe.MatchString(s) {
		return Ref{}, api.Errorf(api.CodeInvalid, "bad image reference %q", s)
	}
	return Ref{Image: s}, nil
}

// Layout holds the dataset and mount paths of the image tree.
type Layout struct {
	Root      string // "<pool>/jailops"
	MountBase string // "/jailops" (directory under which images are mounted)
}

func (l Layout) ImageDS(image string) string { return path.Join(l.Root, "images", image) }
func (l Layout) GenDS(image string, n int) string {
	return fmt.Sprintf("%s/g%d", l.ImageDS(image), n)
}
func (l Layout) StagingDS(image string, n int) string {
	return fmt.Sprintf("%s/staging-g%d", l.ImageDS(image), n)
}
func (l Layout) DeletedParent() string { return path.Join(l.Root, "deleted") }
func (l Layout) DeletedDS(image string, n int) string {
	return fmt.Sprintf("%s/%s-g%d", l.DeletedParent(), image, n)
}
func (l Layout) GenMount(image string, n int) string {
	return fmt.Sprintf("%s/images/%s/g%d", l.MountBase, image, n)
}
func (l Layout) StagingMount(image string, n int) string {
	return fmt.Sprintf("%s/images/%s/staging-g%d", l.MountBase, image, n)
}
