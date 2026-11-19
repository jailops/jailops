package api

import (
	"slices"
	"strconv"
	"strings"
)

// IfMatch is a parsed If-Match header (RFC 9110, section 13.1.1). The server's entity tags are strong and carry the
// generation of the object: "7". A weak tag (W/"7") and a tag that is not a generation never match, as the strong
// comparison of the RFC says.
type IfMatch struct {
	// Any is true for an absent header and for "*": the precondition holds for the object that exists.
	Any bool
	// Gens are the generations that the strong tags of the header name, once each.
	Gens []int64
}

// ParseIfMatch parses the value of an If-Match header: nothing, "*", or a comma separated list of entity tags. An
// entity tag that is not quoted, and "*" inside a list, are invalid.
func ParseIfMatch(h string) (IfMatch, error) {
	h = strings.TrimSpace(h)
	if h == "" || h == "*" {
		return IfMatch{Any: true}, nil
	}
	var m IfMatch
	for _, part := range strings.Split(h, ",") {
		tag := strings.TrimSpace(part)
		weak := strings.HasPrefix(tag, "W/")
		tag = strings.TrimPrefix(tag, "W/")
		if len(tag) < 2 || tag[0] != '"' || tag[len(tag)-1] != '"' {
			return IfMatch{}, Errorf(CodeInvalid, "invalid If-Match %q: an entity tag is quoted (\"7\")", h)
		}
		if weak {
			continue
		}
		n, err := strconv.ParseInt(tag[1:len(tag)-1], 10, 64)
		if err != nil || n < 0 || strconv.FormatInt(n, 10) != tag[1:len(tag)-1] {
			continue // not a tag of ours: it matches no object
		}
		if !slices.Contains(m.Gens, n) {
			m.Gens = append(m.Gens, n)
		}
	}
	return m, nil
}

// Matches says whether the precondition holds for an object of this generation.
func (m IfMatch) Matches(gen int64) bool { return m.Any || slices.Contains(m.Gens, gen) }
