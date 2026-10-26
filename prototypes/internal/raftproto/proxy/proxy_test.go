package proxy

import (
	"net"
	"testing"
	"time"
)

func echoServer(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				b := make([]byte, 16)
				for {
					n, err := c.Read(b)
					if err != nil {
						c.Close()
						return
					}
					c.Write(b[:n])
				}
			}()
		}
	}()
	return l.Addr().String()
}

func TestAllowed(t *testing.T) {
	n := New()
	defer n.Close()
	if !n.Allowed("a", "b") {
		t.Fatal("default connected")
	}
	n.Partition([]string{"a", "b"}, []string{"c"})
	if !n.Allowed("a", "b") || n.Allowed("a", "c") || n.Allowed("c", "b") {
		t.Fatal("groups")
	}
	if n.Allowed("a", "ghost") {
		t.Fatal("unlisted node must be isolated")
	}
	n.Heal()
	if !n.Allowed("a", "c") {
		t.Fatal("heal")
	}
}

func roundtrip(c net.Conn) error {
	c.SetDeadline(time.Now().Add(time.Second))
	if _, err := c.Write([]byte("hi")); err != nil {
		return err
	}
	b := make([]byte, 2)
	_, err := c.Read(b)
	return err
}

func TestPartitionCutsLiveConn(t *testing.T) {
	n := New()
	defer n.Close()
	adv, _ := n.Register("b", echoServer(t))
	c, err := Dial("a")(adv, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := roundtrip(c); err != nil {
		t.Fatal(err)
	}
	n.Partition([]string{"a"}, []string{"b"})
	if err := roundtrip(c); err == nil {
		t.Fatal("live connection should be cut")
	}
	c2, err := Dial("a")(adv, time.Second)
	if err == nil {
		if roundtrip(c2) == nil {
			t.Fatal("new connection must fail")
		}
		c2.Close()
	}
	n.Heal()
	c3, err := Dial("a")(adv, time.Second)
	if err != nil || roundtrip(c3) != nil {
		t.Fatal("healed conn must work", err)
	}
	c3.Close()
}
