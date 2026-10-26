package pfctl

import (
	"context"
	"sort"
	"strings"
	"sync"

	"jailops.net/jailops/sdk/api"
)

// Fake is an in-memory Client. Validate accepts any ruleset except those
// containing the line "!syntax-error" (a hook for failure tests); the
// normalised form is the ruleset itself.
type Fake struct {
	mu      sync.Mutex
	anchors map[string]string
	tables  map[string]map[string]bool // anchor/table -> addrs
	// LoadCalls counts successful loads per anchor.
	LoadCalls map[string]int
	// Killed lists the labels whose states were killed, in order.
	Killed []string
	// ReadErr, when set, is what Read returns (a hook for failure tests).
	ReadErr error
}

var _ Client = (*Fake)(nil)

// NewFake returns an empty Fake.
func NewFake() *Fake {
	return &Fake{anchors: map[string]string{}, tables: map[string]map[string]bool{}, LoadCalls: map[string]int{}}
}

func fakeValidate(anchor, rs string) error {
	if err := ValidateAnchor(anchor); err != nil {
		return err
	}
	if err := validateRuleset(rs); err != nil {
		return err
	}
	for _, l := range strings.Split(rs, "\n") {
		if strings.TrimSpace(l) == "!syntax-error" {
			return api.Errorf(api.CodeInvalid, "pfctl validate: syntax error")
		}
	}
	return nil
}

// Validate implements Client.
func (f *Fake) Validate(_ context.Context, anchor, rs string) (string, error) {
	return rs, fakeValidate(anchor, rs)
}

// Load implements Client. A failed validation leaves the old rules.
func (f *Fake) Load(_ context.Context, anchor, rs string) error {
	if err := fakeValidate(anchor, rs); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.anchors[anchor] = rs
	f.LoadCalls[anchor]++
	return nil
}

// Read implements Client.
func (f *Fake) Read(_ context.Context, anchor string) (string, error) {
	if err := ValidateAnchor(anchor); err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ReadErr != nil {
		return "", f.ReadErr
	}
	return f.anchors[anchor], nil
}

// Flush implements Client.
func (f *Fake) Flush(_ context.Context, anchor string) error {
	if err := ValidateAnchor(anchor); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.anchors, anchor)
	for k := range f.tables {
		if strings.HasPrefix(k, anchor+"\x00") {
			delete(f.tables, k)
		}
	}
	return nil
}

func (f *Fake) mod(anchor, table string, addrs []string, add bool) error {
	if err := ValidateAnchor(anchor); err != nil {
		return err
	}
	if err := ValidateTable(table); err != nil {
		return err
	}
	if len(addrs) == 0 {
		return api.Errorf(api.CodeInvalid, "no addresses given")
	}
	norm := make([]string, len(addrs))
	for i, a := range addrs {
		n, err := ValidateAddr(a)
		if err != nil {
			return err
		}
		norm[i] = n
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	k := anchor + "\x00" + table
	if add && f.tables[k] == nil {
		f.tables[k] = map[string]bool{}
	}
	for _, n := range norm {
		if add {
			f.tables[k][n] = true
		} else if f.tables[k] != nil {
			delete(f.tables[k], n)
		}
	}
	return nil
}

// TableAdd implements Client.
func (f *Fake) TableAdd(_ context.Context, anchor, table string, addrs ...string) error {
	return f.mod(anchor, table, addrs, true)
}

// TableDelete implements Client.
func (f *Fake) TableDelete(_ context.Context, anchor, table string, addrs ...string) error {
	return f.mod(anchor, table, addrs, false)
}

// TableShow implements Client; a missing table is CodeNotFound like pfctl.
func (f *Fake) TableShow(_ context.Context, anchor, table string) ([]string, error) {
	if err := ValidateAnchor(anchor); err != nil {
		return nil, err
	}
	if err := ValidateTable(table); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tables[anchor+"\x00"+table]
	if !ok {
		return nil, api.Errorf(api.CodeNotFound, "table %s does not exist in anchor %s", table, anchor)
	}
	out := make([]string, 0, len(t))
	for a := range t {
		out = append(out, a)
	}
	sort.Strings(out)
	return out, nil
}

// KillLabel implements Client; the labels are kept in Killed.
func (f *Fake) KillLabel(_ context.Context, label string) error {
	if err := ValidateLabel(label); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Killed = append(f.Killed, label)
	return nil
}
