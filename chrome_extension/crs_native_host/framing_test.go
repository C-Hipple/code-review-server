package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	msgs := [][]byte{
		[]byte(`{}`),
		[]byte(`{"method":"RPCHandler.Hello","params":[{}],"id":1}`),
		[]byte(`{"text":"ünïcödé 日本語 🎉"}`),
		bytes.Repeat([]byte("x"), maxOutgoingFrame),
	}
	var buf bytes.Buffer
	for _, msg := range msgs {
		if err := writeFrame(&buf, msg); err != nil {
			t.Fatalf("writeFrame(%d bytes): %v", len(msg), err)
		}
	}
	for i, want := range msgs {
		got, err := readFrame(&buf, maxIncomingFrame)
		if err != nil {
			t.Fatalf("readFrame #%d: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("frame #%d: got %d bytes, want %d", i, len(got), len(want))
		}
	}
	if _, err := readFrame(&buf, maxIncomingFrame); err != io.EOF {
		t.Errorf("after the last frame: got %v, want io.EOF", err)
	}
}

func TestFrameHeaderIsLittleEndianLength(t *testing.T) {
	var buf bytes.Buffer
	if err := writeFrame(&buf, []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint32(buf.Bytes()[:4]); got != 7 {
		t.Errorf("header = %d, want 7", got)
	}
}

func TestWriteFrameRejectsOversizedMessage(t *testing.T) {
	var buf bytes.Buffer
	err := writeFrame(&buf, bytes.Repeat([]byte("x"), maxOutgoingFrame+1))
	if !errors.Is(err, errFrameTooLarge) {
		t.Fatalf("got %v, want errFrameTooLarge", err)
	}
	if buf.Len() != 0 {
		t.Errorf("wrote %d bytes of a refused frame", buf.Len())
	}
}

func TestReadFrameRejectsOversizedFrame(t *testing.T) {
	var header [4]byte
	binary.NativeEndian.PutUint32(header[:], maxIncomingFrame+1)
	// No body follows: the length alone must be enough to refuse it, without
	// allocating or reading 64 MiB.
	_, err := readFrame(bytes.NewReader(header[:]), maxIncomingFrame)
	if !errors.Is(err, errFrameTooLarge) {
		t.Fatalf("got %v, want errFrameTooLarge", err)
	}
}

func TestReadFrameTruncated(t *testing.T) {
	for name, input := range map[string][]byte{
		"header": {7, 0},
		"body":   append([]byte{7, 0, 0, 0}, `{"a"`...),
	} {
		_, err := readFrame(bytes.NewReader(input), maxIncomingFrame)
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("truncated %s: got %v, want io.ErrUnexpectedEOF", name, err)
		}
		if errors.Is(err, io.EOF) {
			t.Errorf("truncated %s reads as a clean EOF", name)
		}
	}
}

func TestReadFrameEmptyStreamIsEOF(t *testing.T) {
	if _, err := readFrame(strings.NewReader(""), maxIncomingFrame); err != io.EOF {
		t.Errorf("got %v, want io.EOF", err)
	}
}
