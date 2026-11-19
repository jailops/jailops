package api

import (
	"slices"
	"strconv"
	"strings"
)

// ETag is the entity tag of the object with this UUID at this generation: a strong validator, `"<uuid>-<generation>"`.
// It names one incarnation of the object at one revision, so a change or delete sent with If-Match fails when the object
// was changed since (another generation) and when it was deleted and created again under the same name (another UUID).
// Clients treat the tag as opaque: they send back what the server gave them.
func ETag(uuid string, generation int64) string {
	return `"` + uuid + "-" + strconv.FormatInt(generation, 10) + `"`
}

// ParseETag splits a strong entity tag written by ETag. ok is false for any other string.
func ParseETag(tag string) (uuid string, generation int64, ok bool) {
	if len(tag) < 2 || tag[0] != '"' || tag[len(tag)-1] != '"' {
		return "", 0, false
	}
	body := tag[1 : len(tag)-1]
	i := strings.LastIndexByte(body, '-')
	if i <= 0 {
		return "", 0, false
	}
	n, err := strconv.ParseInt(body[i+1:], 10, 64)
	if err != nil || n < 0 || strconv.FormatInt(n, 10) != body[i+1:] {
		return "", 0, false
	}
	return body[:i], n, true
}

// IfMatch is a parsed If-Match header (RFC 9110, section 13.1.1). A weak tag (W/"...") never matches, as the strong
// comparison of the RFC says.
type IfMatch struct {
	// Any is true for an absent header and for "*": the precondition holds for the object that exists.
	Any bool
	// Tags are the strong entity tags of the header, quoted as sent, once each.
	Tags []string
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
			return IfMatch{}, Errorf(CodeInvalid, "invalid If-Match %q: an entity tag is quoted", h)
		}
		if !weak && !slices.Contains(m.Tags, tag) {
			m.Tags = append(m.Tags, tag)
		}
	}
	return m, nil
}

// Matches says whether the precondition holds for the object with this entity tag.
func (m IfMatch) Matches(etag string) bool { return m.Any || slices.Contains(m.Tags, etag) }
