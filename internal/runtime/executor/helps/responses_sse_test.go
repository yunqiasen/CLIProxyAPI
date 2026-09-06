package helps

import (
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func TestResponsesSSEReader(t *testing.T) {
	for _, raw := range []string{
		"event: response.completed\ndata: {\"type\":\ndata: \"response.completed\"}\n\n",
		"data: {\"type\":\"response.completed\"}\r\n\r\n",
		"data: {\"type\":\"response.completed\"}",
	} {
		r := NewResponsesSSEReader(strings.NewReader(raw), 1024)
		f, err := r.Next()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(f.Data), "response.completed") {
			t.Fatalf("missing data: %q", f.Data)
		}
		if _, err = r.Next(); err != io.EOF {
			t.Fatalf("tail error=%v", err)
		}
	}
	t.Run("legacy independent JSON lines", func(t *testing.T) {
		r := NewResponsesSSEReader(strings.NewReader("data: {\"type\":\"a\"}\ndata: {\"type\":\"b\"}\n\n"), 1024)
		a, err := r.Next()
		if err != nil {
			t.Fatal(err)
		}
		b, err := r.Next()
		if err != nil {
			t.Fatal(err)
		}
		if string(a.Data) != `{"type":"a"}` || string(b.Data) != `{"type":"b"}` {
			t.Fatal("legacy records merged")
		}
	})
	t.Run("aggregate bound", func(t *testing.T) {
		r := NewResponsesSSEReader(strings.NewReader("data: {\ndata: "+strings.Repeat(" ", 200)+"}\n\n"), 100)
		if _, err := r.Next(); err == nil {
			t.Fatal("oversized frame accepted")
		}
	})
}

func TestResponsesBootstrapBoundAndCommit(t *testing.T) {
	var b ResponsesStreamBootstrap
	if got := b.Push("response.created", [][]byte{[]byte("created")}); len(got) != 0 || b.Committed() {
		t.Fatal("handshake committed immediately")
	}
	got := b.Push("response.output_item.added", [][]byte{[]byte("item")})
	if len(got) != 2 || !b.Committed() {
		t.Fatal("first output did not flush handshake")
	}
	b.Reset()
	for i := 0; i < 16; i++ {
		b.Push("", [][]byte{[]byte(": ping\n\n")})
	}
	if !b.Committed() {
		t.Fatal("heartbeat-only prefix remained unbounded")
	}
}

func TestResponsesSSEReaderTrailingEventField(t *testing.T) {
	for _, raw := range []string{
		"data: {\"response\":{\"output\":[]}}\nevent: response.completed\n\n",
		"event: ignored\ndata: {\"response\":{\"output\":[]}}\nevent: response.completed\n\n",
	} {
		r := NewResponsesSSEReader(strings.NewReader(raw), 1024)
		frame, err := r.Next()
		if err != nil || frame.Event != "response.completed" || len(frame.Data) == 0 {
			t.Fatalf("trailing event field lost: frame=%+v err=%v", frame, err)
		}
	}
}

func TestResponsesSSEReaderByteFragments(t *testing.T) {
	raw := "event: response.completed\r\ndata: {\"type\":\r\ndata: \"response.completed\",\"text\":\"中文\"}\r\n\r\n"
	reader := NewResponsesSSEReader(iotest.OneByteReader(strings.NewReader(raw)), 1024)
	frame, err := reader.Next()
	if err != nil || frame.Event != "response.completed" || !strings.Contains(string(frame.Data), "中文") {
		t.Fatalf("fragmented frame lost: %v", err)
	}
	if _, err = reader.Next(); err != io.EOF {
		t.Fatalf("unexpected tail: %v", err)
	}
}
