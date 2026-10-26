package api

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

func TestFramesRoundTripAndBounds(t *testing.T) {
	var buf bytes.Buffer
	big := bytes.Repeat([]byte("x"), MaxFrame)
	for _, f := range []struct {
		t FrameType
		p []byte
	}{{FrameStdin, []byte("ls\n")}, {FrameStdin, nil}, {FrameResize, ResizePayload(24, 80)}, {FrameSignal, Int32Payload(15)}, {FrameStdout, big}, {FrameExit, Int32Payload(-1)}} {
		if err := WriteFrame(&buf, f.t, f.p); err != nil {
			t.Fatal(err)
		}
	}
	r := bufio.NewReader(&buf)
	typ, p, err := ReadFrame(r)
	if err != nil || typ != FrameStdin || string(p) != "ls\n" {
		t.Fatal(typ, p, err)
	}
	if typ, p, _ = ReadFrame(r); typ != FrameStdin || len(p) != 0 {
		t.Fatal("end of input frame", typ, p)
	}
	_, p, _ = ReadFrame(r)
	if rows, cols, err := ParseResize(p); err != nil || rows != 24 || cols != 80 {
		t.Fatal(rows, cols, err)
	}
	_, p, _ = ReadFrame(r)
	if n, _ := ParseInt32(p); n != 15 {
		t.Fatal(n)
	}
	if _, p, _ = ReadFrame(r); len(p) != MaxFrame {
		t.Fatal(len(p))
	}
	_, p, _ = ReadFrame(r)
	if n, _ := ParseInt32(p); n != -1 {
		t.Fatal(n)
	}
	if err := WriteFrame(&buf, FrameStdout, append(big, 'x')); err == nil {
		t.Fatal("an oversized frame was written")
	}
	// A hostile length is refused without reading it.
	hdr := []byte{byte(FrameStdout), 0xff, 0xff, 0xff, 0xff}
	if _, _, err := ReadFrame(bufio.NewReader(bytes.NewReader(hdr))); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatal(err)
	}
	if _, _, err := ReadFrame(bufio.NewReader(bytes.NewReader([]byte{99, 0, 0, 0, 0}))); err == nil {
		t.Fatal("unknown type")
	}
	if _, _, err := ReadFrame(bufio.NewReader(bytes.NewReader([]byte{2, 0, 0, 0, 5, 'a'}))); err == nil {
		t.Fatal("truncated frame")
	}
}
