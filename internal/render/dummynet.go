package render

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"
	"strconv"
)

// The bandwidth limit of a jail's device is a dummynet pipe, and it limits what the jail SENDS (egress). pf sends the
// device's packets into the pipe with dnpipe on match rules of the jail's anchor.
//
// Only the direction out of the jail can be shaped this way. A packet that is sent into a dummynet pipe and comes back
// is put into the IP stack again, which is right for what the jail sends (it arrives on the jail's own port, a bridge
// member, and goes on through the host's IP stack), but wrong at the hooks that a packet on its way TO the jail meets
// after the gateway interface: the gateway's bridge member and the jail's own port, where pf matches the state again
// and sends the packet into the pipe again, which routes it back to the gateway interface. The packet circulates
// until the pipe's queue drops it, and the connection stalls. Traffic into the jail therefore carries no pipe: a rule
// names it with a zero (dnpipe (0, N)), so that the state of a flow that the host or the internet started still shapes
// the jail's answers.
//
// The pipe numbers are JailOps's own: PipeBase to PipeBase+PipeSpan-1. Nothing else on the host may use them, and the
// reconciler deletes a pipe in that range that no jail wants.
const (
	PipeBase = 40000
	PipeSpan = 20000 // 20000 devices with a limit
)

// PipeKey names a device with a limit: the jail's stable identity and the index of the device in its network list.
type PipeKey struct {
	Jail   string
	Device int
}

// PipeNumbers gives every key a pipe number. It comes from a hash of the key, and a key whose slot is taken goes to the
// next free one, in the order of the keys, so the numbers are the same on every call for the same set of keys and no
// two keys share a pipe.
func PipeNumbers(keys []PipeKey) (map[PipeKey]int, error) {
	if len(keys) > PipeSpan {
		return nil, fmt.Errorf("render: %d devices have a bandwidth limit, at most %d are possible", len(keys), PipeSpan)
	}
	sorted := append([]PipeKey(nil), keys...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Jail != sorted[j].Jail {
			return sorted[i].Jail < sorted[j].Jail
		}
		return sorted[i].Device < sorted[j].Device
	})
	used := map[int]bool{}
	out := make(map[PipeKey]int, len(sorted))
	for _, k := range sorted {
		if _, dup := out[k]; dup {
			continue
		}
		sum := sha256.Sum256([]byte("jailops-pipe\x00" + k.Jail + "\x00" + strconv.Itoa(k.Device)))
		slot := int(binary.BigEndian.Uint32(sum[:4])) % PipeSpan
		for used[slot] {
			slot = (slot + 1) % PipeSpan
		}
		used[slot] = true
		out[k] = PipeBase + slot
	}
	return out, nil
}

// IsManagedPipe says whether a pipe number is in JailOps's range.
func IsManagedPipe(n int) bool { return n >= PipeBase && n < PipeBase+PipeSpan }

// Shape is the egress limit of one device of a jail, as its anchor needs it: the jail's host-side end of the device's
// epair (what the jail sends arrives here), the gateway interface of the device's network and the device's addresses
// (what the host or the internet started and the jail answers is found by its state), and the pipe.
type Shape struct {
	Iface   string
	Gateway string
	Addrs   []string
	Pipe    int
}

// PipeRate is a bandwidth limit as dnctl takes it: "10000000bit/s".
func PipeRate(bits uint64) string { return strconv.FormatUint(bits, 10) + "bit/s" }
