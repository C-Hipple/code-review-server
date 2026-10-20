package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSplitRunesNeverSplitsARune(t *testing.T) {
	inputs := []string{
		"plain ascii text that is long enough to split",
		"ünïcödé ünïcödé ünïcödé",
		"日本語のテキスト日本語のテキスト",
		"🎉🎉🎉 mixed ü 日 🎉 text \u2028 here",
	}
	for _, input := range inputs {
		for limit := utf8.UTFMax; limit <= 9; limit++ {
			pieces := splitRunes([]byte(input), limit)
			var joined []byte
			for _, p := range pieces {
				if len(p) == 0 || len(p) > limit {
					t.Errorf("%q limit %d: piece of %d bytes", input, limit, len(p))
				}
				if !utf8.Valid(p) {
					t.Errorf("%q limit %d: piece %q splits a rune", input, limit, p)
				}
				joined = append(joined, p...)
			}
			if string(joined) != input {
				t.Errorf("%q limit %d: pieces join to %q", input, limit, joined)
			}
		}
	}
}

func TestSplitRunesInvalidUTF8StillProgresses(t *testing.T) {
	input := bytes.Repeat([]byte{0x80}, 10) // continuation bytes only
	pieces := splitRunes(input, 4)
	if got := bytes.Join(pieces, nil); !bytes.Equal(got, input) {
		t.Fatalf("pieces join to %x", got)
	}
	for _, p := range pieces {
		if len(p) == 0 || len(p) > 4 {
			t.Errorf("piece of %d bytes", len(p))
		}
	}
}

// bigResponse builds a JSON-RPC response line of at least size bytes. A
// prefix of two bytes puts the 3- and 4-byte runes that follow off the chunk
// boundaries, so cuts have to back up.
func bigResponse(t *testing.T, size int) []byte {
	t.Helper()
	unit := `ü 日本語 🎉 <a href=\"x\">&amp;</a> \\ \" ` + "\u2028" + ` `
	var b strings.Builder
	b.WriteString(`{"id":7,"result":{"text":"xy`)
	for b.Len() < size {
		b.WriteString(unit)
	}
	b.WriteString(`"},"error":null}`)
	line := []byte(b.String())
	if !json.Valid(line) {
		t.Fatal("test response is not valid JSON")
	}
	return line
}

func TestChunkFramesReassemble(t *testing.T) {
	line := bigResponse(t, 3*maxVerbatimLine)
	frames, err := chunkFrames(4, line)
	if err != nil {
		t.Fatal(err)
	}
	if want := (len(line) + chunkPieceBytes - 1) / chunkPieceBytes; len(frames) < want {
		t.Fatalf("%d frames for %d bytes, want at least %d", len(frames), len(line), want)
	}
	var joined strings.Builder
	for i, frame := range frames {
		if len(frame) >= maxOutgoingFrame {
			t.Errorf("frame %d is %d bytes", i, len(frame))
		}
		var msg chunkMessage
		if err := json.Unmarshal(frame, &msg); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		c := msg.Chunk
		if c.Stream != 4 || c.Seq != i || c.Total != len(frames) {
			t.Errorf("frame %d: stream %d seq %d total %d", i, c.Stream, c.Seq, c.Total)
		}
		if len(c.Data) > chunkPieceBytes {
			t.Errorf("frame %d carries %d bytes", i, len(c.Data))
		}
		joined.WriteString(c.Data)
	}
	if joined.String() != string(line) {
		t.Fatal("reassembled chunks differ from the response")
	}
}

func TestChunkFramesWorstCaseEscapingFits(t *testing.T) {
	// Backslashes, quotes and U+2028 are the bytes that grow most when a
	// piece is re-encoded as a JSON string.
	var b strings.Builder
	b.WriteString(`{"id":1,"result":"`)
	for b.Len() < 2*maxVerbatimLine {
		b.WriteString(`\\\"` + "\u2028")
	}
	b.WriteString(`"}`)
	frames, err := chunkFrames(1, []byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	for i, frame := range frames {
		if len(frame) > maxOutgoingFrame {
			t.Errorf("frame %d is %d bytes", i, len(frame))
		}
	}
}

func TestChunkFramesDoNotEscapeHTML(t *testing.T) {
	frames, err := chunkFrames(1, []byte(`{"html":"<b>&</b>"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"crs_chunk":{"stream":1,"seq":0,"total":1,"data":"{\"html\":\"<b>&</b>\"}"}}`
	if len(frames) != 1 || string(frames[0]) != want {
		t.Errorf("got %s\nwant %s", frames, want)
	}
}

func TestHostEventShapes(t *testing.T) {
	for _, tc := range []struct {
		ev   hostEvent
		want string
	}{
		{
			hostEvent{Event: eventReady, ServerPath: "/bin/codereviewserver", LogPath: "/l.log", Version: "0.1.0"},
			`{"crs_host":{"event":"ready","server_path":"/bin/codereviewserver","log_path":"/l.log","version":"0.1.0"}}`,
		},
		{
			hostEvent{Event: eventError, Message: "codereviewserver not found", LogPath: "/l.log"},
			`{"crs_host":{"event":"error","message":"codereviewserver not found","log_path":"/l.log"}}`,
		},
		{
			hostEvent{Event: eventServerExited, Message: "exit status 1", LogPath: "/l.log"},
			`{"crs_host":{"event":"server_exited","message":"exit status 1","log_path":"/l.log"}}`,
		},
	} {
		got, err := encodeMessage(hostMessage{Host: tc.ev})
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != tc.want {
			t.Errorf("got  %s\nwant %s", got, tc.want)
		}
	}
}

func TestCheckResponse(t *testing.T) {
	if _, err := checkResponse([]byte(`{"id":1,"result":null,"error":null}`)); err != nil {
		t.Errorf("valid response rejected: %v", err)
	}
	for _, bad := range []string{`starting up`, `[1,2]`, `"str"`, `{"id":1`} {
		if _, err := checkResponse([]byte(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	got, err := checkResponse([]byte("{\"s\":\"a\xffb\"}"))
	if err != nil || string(got) != "{\"s\":\"a\uFFFDb\"}" {
		t.Errorf("invalid UTF-8: got %q, %v", got, err)
	}
}

func TestResponseID(t *testing.T) {
	for line, want := range map[string]string{
		`{"id":12,"result":{},"error":null}`: "12",
		`{"id":"abc","result":1}`:            `"abc"`,
		`{"result":1,"id":3}`:                "?",
		`not json`:                           "?",
	} {
		if got := responseID([]byte(line)); got != want {
			t.Errorf("responseID(%s) = %s, want %s", line, got, want)
		}
	}
}

func TestRequestLine(t *testing.T) {
	got, err := requestLine([]byte("{ \"method\" : \"RPCHandler.GetPR\",\n \"params\" : [ { \"Body\": \"a\\nb\" } ], \"id\" : 3 }"))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"method":"RPCHandler.GetPR","params":[{"Body":"a\nb"}],"id":3}` + "\n"
	if string(got) != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	for _, bad := range []string{`[1]`, `"x"`, `{`, ``} {
		if _, err := requestLine([]byte(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestDescribeRequestLeavesOutParams(t *testing.T) {
	got := describeRequest([]byte(`{"method":"RPCHandler.AddComment","params":[{"Body":"secret"}],"id":9}`))
	if got != `"RPCHandler.AddComment" id=9` {
		t.Errorf("got %s", got)
	}
}
