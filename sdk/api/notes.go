package api

// MaxNotes is the longest notes text of an object, in bytes.
const MaxNotes = 64 << 10

// ValidNotes checks the notes of an object: at most MaxNotes bytes of valid UTF-8 without control or invisible
// characters other than the line feed and the tab.
func ValidNotes(s string) error {
	if len(s) > MaxNotes {
		return Errorf(CodeInvalid, "notes are %d bytes, at most %d", len(s), MaxNotes).
			WithHint("keep long documents elsewhere and link them from the notes")
	}
	if HasUnsafeText(s, "\n\t") {
		return Errorf(CodeInvalid, "notes hold control or invisible characters").
			WithHint("notes are text: line feeds and tabs are allowed, carriage returns and escape sequences are not")
	}
	return nil
}

// Noted is a specification that carries notes, a long text about the object.
type Noted interface{ NotesText() string }

// CheckNotes validates the notes of a specification that has them.
func CheckNotes(spec any) error {
	if n, ok := spec.(Noted); ok {
		return ValidNotes(n.NotesText())
	}
	return nil
}

// NotesText returns the notes of a specification (see Noted).
func (s JailSpec) NotesText() string           { return s.Notes }
func (s NetworkSpec) NotesText() string        { return s.Notes }
func (s VolumeSpec) NotesText() string         { return s.Notes }
func (s ProjectSpec) NotesText() string        { return s.Notes }
func (s StorageSpec) NotesText() string        { return s.Notes }
func (s FirewallPolicySpec) NotesText() string { return s.Notes }
func (s AddressSetSpec) NotesText() string     { return s.Notes }
func (s ImageSpec) NotesText() string          { return s.Notes }
func (s ProfileSpec) NotesText() string        { return s.Notes }
