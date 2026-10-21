package app

import (
	"bytes"
	"testing"
)

type partialFrameWriter struct {
	bytes.Buffer
	max int
}

func (w *partialFrameWriter) Write(p []byte) (int, error) {
	if len(p) > w.max {
		p = p[:w.max]
	}
	return w.Buffer.Write(p)
}

func TestWriteFrameHandlesShortWrites(t *testing.T) {
	var w partialFrameWriter
	w.max = 2
	payload := []byte("terminal output")
	if err := writeFrame(&w, frameOutput, payload); err != nil {
		t.Fatal(err)
	}

	want := append([]byte{frameOutput, 0, 0, 0, byte(len(payload))}, payload...)
	if !bytes.Equal(w.Bytes(), want) {
		t.Fatalf("frame = %q, want %q", w.Bytes(), want)
	}
}
