package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

const (
	// maxVerbatimLine is the largest server response forwarded to Chrome as
	// is. It leaves headroom under maxOutgoingFrame; anything bigger is
	// chunked.
	maxVerbatimLine = 900_000
	// chunkPieceBytes bounds the raw slice of a response one chunk carries.
	// A response is valid JSON and valid UTF-8, so re-encoding a piece as a
	// JSON string at most doubles it (quotes, backslashes, \t, \r and
	// U+2028/U+2029 are the only characters that grow), keeping every chunk
	// frame near half of maxOutgoingFrame.
	chunkPieceBytes = 256 << 10
)

// Host event names, the "event" field of a crs_host message.
const (
	eventReady        = "ready"
	eventError        = "error"
	eventServerExited = "server_exited"
)

// hostEvent tells the extension what the host itself is doing, as opposed
// to a JSON-RPC response from the server.
type hostEvent struct {
	Event      string `json:"event"`
	Message    string `json:"message,omitempty"`
	ServerPath string `json:"server_path,omitempty"`
	LogPath    string `json:"log_path"`
	Version    string `json:"version,omitempty"`
}

type hostMessage struct {
	Host hostEvent `json:"crs_host"`
}

// chunk is one piece of a server response too large for a single frame.
// The extension concatenates Data across Seq 0..Total-1 of a Stream and
// parses the result as the response.
type chunk struct {
	Stream int    `json:"stream"`
	Seq    int    `json:"seq"`
	Total  int    `json:"total"`
	Data   string `json:"data"`
}

type chunkMessage struct {
	Chunk chunk `json:"crs_chunk"`
}

// encodeMessage marshals v without escaping <, > and &: Chrome doesn't need
// it, and the escapes would grow a chunk's data sixfold for those bytes.
func encodeMessage(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// chunkFrames splits line into crs_chunk messages for stream. Every piece
// ends on a rune boundary, so each one's data is a valid string on its own
// and the extension's concatenation reproduces line exactly.
func chunkFrames(stream int, line []byte) ([][]byte, error) {
	pieces := splitRunes(line, chunkPieceBytes)
	frames := make([][]byte, 0, len(pieces))
	for seq, piece := range pieces {
		frame, err := encodeMessage(chunkMessage{Chunk: chunk{
			Stream: stream,
			Seq:    seq,
			Total:  len(pieces),
			Data:   string(piece),
		}})
		if err != nil {
			return nil, fmt.Errorf("encoding chunk %d of stream %d: %w", seq, stream, err)
		}
		if len(frame) > maxOutgoingFrame {
			return nil, fmt.Errorf("chunk %d of stream %d: %w: %d bytes", seq, stream, errFrameTooLarge, len(frame))
		}
		frames = append(frames, frame)
	}
	return frames, nil
}

// splitRunes cuts b into pieces of at most limit bytes, backing each cut up
// to the start of a UTF-8 sequence so no rune is split across pieces. A cut
// that finds no rune start within utf8.UTFMax bytes (invalid UTF-8) is made
// at the limit.
func splitRunes(b []byte, limit int) [][]byte {
	var pieces [][]byte
	for len(b) > limit {
		cut := limit
		for back := 0; back < utf8.UTFMax && cut > 0 && !utf8.RuneStart(b[cut]); back++ {
			cut--
		}
		if cut == 0 || !utf8.RuneStart(b[cut]) {
			cut = limit
		}
		pieces = append(pieces, b[:cut])
		b = b[cut:]
	}
	if len(b) > 0 {
		pieces = append(pieces, b)
	}
	return pieces
}
