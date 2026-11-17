package dnsprobe

import (
	"context"
	"net"
	"testing"
	"time"
)

// server answers each question with the given rcode (or stays silent when rcode is -1); a first packet with the wrong
// id is sent before the real answer.
func server(t *testing.T, rcode int) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skip("no loopback UDP here:", err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if rcode < 0 || n < 12 {
				continue
			}
			bad := append([]byte(nil), buf[:n]...)
			bad[0] ^= 0xff // another id: must be ignored
			bad[2] |= 0x80
			_, _ = pc.WriteTo(bad, from)
			ans := append([]byte(nil), buf[:n]...)
			ans[2] |= 0x80
			ans[3] = ans[3]&0xf0 | byte(rcode)
			_, _ = pc.WriteTo(ans, from)
		}
	}()
	return pc.LocalAddr().String()
}

func TestProbeStatuses(t *testing.T) {
	cases := []struct {
		name  string
		rcode int
		want  Status
		up    bool
	}{
		{"answers", 0, Answered, true},
		{"name error is an answer", 3, Answered, true},
		{"refused", 5, Refused, false},
		{"servfail", 2, ServFail, true},
		{"silent", -1, NoReply, false},
	}
	for _, c := range cases {
		addr := server(t, c.rcode)
		r := Query(context.Background(), addr, "localhost.", TypeA, 300*time.Millisecond)
		if r.Status != c.want || r.Up() != c.up {
			t.Errorf("%s: %+v, want status %d up %v", c.name, r, c.want, c.up)
		}
	}
}

func TestWaitTriesAgain(t *testing.T) {
	calls := 0
	f := func(context.Context, string, string, uint16, time.Duration) Result {
		calls++
		if calls < 3 {
			return Result{}
		}
		return Result{Status: Answered}
	}
	if r := Wait(context.Background(), f, "127.0.0.1", "localhost.", TypeA, 3, time.Millisecond); !r.Up() || calls != 3 {
		t.Fatalf("%+v after %d calls", r, calls)
	}
}
