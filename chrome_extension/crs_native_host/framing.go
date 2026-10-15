package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	// maxIncomingFrame is the largest message Chrome sends a native host.
	// A length beyond it means the stream is not what we think it is, so it
	// is an error rather than something to skip over.
	maxIncomingFrame = 64 << 20
	// maxOutgoingFrame is the largest message Chrome accepts from a native
	// host. Chrome drops the connection on a bigger one, which is why large
	// server responses are chunked (chunk.go).
	maxOutgoingFrame = 1 << 20
)

// errFrameTooLarge marks a frame over the size limit in either direction.
var errFrameTooLarge = errors.New("native messaging frame too large")

// readFrame reads one native messaging message: a 4-byte length in the
// platform's native byte order (little-endian everywhere Chrome ships),
// then that many bytes of UTF-8 JSON. It returns io.EOF only when the stream
// ends cleanly between frames.
func readFrame(r io.Reader, limit int) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, fmt.Errorf("reading frame header: %w", err)
		}
		return nil, err
	}
	n := binary.NativeEndian.Uint32(header[:])
	if uint64(n) > uint64(limit) {
		return nil, fmt.Errorf("%w: %d bytes (limit %d)", errFrameTooLarge, n, limit)
	}
	msg := make([]byte, n)
	if _, err := io.ReadFull(r, msg); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return nil, fmt.Errorf("reading %d-byte frame: %w", n, err)
	}
	return msg, nil
}

// writeFrame writes msg as one native messaging message. The header and body
// go out in a single Write so a reader never sees half a frame from us.
func writeFrame(w io.Writer, msg []byte) error {
	if len(msg) > maxOutgoingFrame {
		return fmt.Errorf("%w: %d bytes (limit %d)", errFrameTooLarge, len(msg), maxOutgoingFrame)
	}
	buf := make([]byte, 4+len(msg))
	binary.NativeEndian.PutUint32(buf, uint32(len(msg)))
	copy(buf[4:], msg)
	_, err := w.Write(buf)
	return err
}
