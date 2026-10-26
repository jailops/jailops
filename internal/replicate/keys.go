package replicate

import (
	"sort"
	"time"

	"jailops.net/jailops/sdk/api"
)

// Key model (design section 9.8): each host has its own wrapping key and an
// encryption root; jail data datasets are natively encrypted. A raw send keeps
// the replica encrypted at rest and in transit; running the jail on the
// receiving host needs the key to be loaded there. Key loss is data loss, so a
// raw replica of encrypted data is only accepted once the key is escrowed.

// KeyState is the state of the host key of an encrypted jail.
type KeyState string

const (
	// KeyNone: no key is recorded.
	KeyNone KeyState = "none"
	// KeyPresent: the key exists on the host but no complete escrow exists.
	KeyPresent KeyState = "present"
	// KeyEscrowed: the key and every earlier key are in an escrow blob that
	// offline recovery recipients can open.
	KeyEscrowed KeyState = "escrowed"
)

// Escrow describes an escrow blob. The blob is age-encrypted to offline
// recovery recipients and is not secret; this descriptor holds no key.
type Escrow struct {
	// KeyID identifies the current key.
	KeyID string
	// Covers lists every key id inside the blob: the current key and all
	// earlier ones.
	Covers []string
	// Recipients are the age public keys the blob is encrypted to.
	Recipients []string
	// BlobRef names where the blob is stored (stored next to the replicas).
	BlobRef   string
	CreatedAt time.Time
}

// KeyEntry is one key in a host's history.
type KeyEntry struct {
	KeyID string
	// From is when the key became current (creation or a change-key).
	From time.Time
}

// KeyHistory lists keys oldest first. A zfs change-key re-wraps without
// re-encrypting data, so a replica made before a rotation needs the key that was
// current when it was taken.
type KeyHistory []KeyEntry

// KeyAt returns the key id that was current at t, and false if t predates the
// history.
func (h KeyHistory) KeyAt(t time.Time) (string, bool) {
	hs := append(KeyHistory(nil), h...)
	sort.Slice(hs, func(i, j int) bool { return hs[i].From.Before(hs[j].From) })
	id, ok := "", false
	for _, e := range hs {
		if !e.From.After(t) {
			id, ok = e.KeyID, true
		}
	}
	return id, ok
}

// IDs returns every key id in the history.
func (h KeyHistory) IDs() []string {
	var ids []string
	for _, e := range h {
		ids = append(ids, e.KeyID)
	}
	return ids
}

// KeyStatus is the recorded key situation of one encrypted jail.
type KeyStatus struct {
	State   KeyState
	KeyID   string
	History KeyHistory
	Escrow  *Escrow
}

// EscrowComplete reports whether the status really is escrowed: the state says
// so and the descriptor names recipients, a blob and every key in the history.
// A state that claims more than the descriptor proves does not count.
func (k KeyStatus) EscrowComplete() bool {
	e := k.Escrow
	if k.State != KeyEscrowed || e == nil || len(e.Recipients) == 0 || e.BlobRef == "" {
		return false
	}
	covers := map[string]bool{}
	for _, c := range e.Covers {
		covers[c] = true
	}
	if k.KeyID != "" && (e.KeyID != k.KeyID || !covers[k.KeyID]) {
		return false
	}
	for _, id := range k.History.IDs() {
		if !covers[id] {
			return false
		}
	}
	return true
}

// CheckRecoverable decides whether a replica may be taken. Raw sends of
// encrypted data need a complete escrow; other streams carry no key question.
func CheckRecoverable(raw, encrypted bool, k KeyStatus) error {
	if !raw || !encrypted || k.EscrowComplete() {
		return nil
	}
	state := k.State
	if state == "" {
		state = KeyNone
	}
	return api.Errorf(api.CodeInvalid, "raw replication refused: the key is not escrowed (state %s)", state).
		WithDetail("key_state", string(state)).
		WithHint("run `jailops key escrow` and store the blob next to the replicas before the first encrypted replication")
}
