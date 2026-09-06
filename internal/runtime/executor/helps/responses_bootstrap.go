package helps

import "bytes"

// ResponsesStreamBootstrap withholds lifecycle-only frames until output commits.
// A bounded prefix preserves liveness for relays that send only heartbeats.
type ResponsesStreamBootstrap struct {
	pending      [][]byte
	size, frames int
	committed    bool
}

func ResponsesLifecycleEvent(event string) bool {
	return event == "" || event == "keepalive" || event == "response.created" || event == "response.in_progress" || event == "response.queued"
}
func (b *ResponsesStreamBootstrap) Committed() bool { return b.committed }
func (b *ResponsesStreamBootstrap) Reset()          { *b = ResponsesStreamBootstrap{} }
func (b *ResponsesStreamBootstrap) Push(event string, chunks [][]byte) [][]byte {
	if b.committed {
		return chunks
	}
	b.frames++
	for _, chunk := range chunks {
		b.pending = append(b.pending, bytes.Clone(chunk))
		b.size += len(chunk)
	}
	if ResponsesLifecycleEvent(event) && b.frames < 16 && b.size < 64*1024 {
		return nil
	}
	b.committed = true
	out := b.pending
	b.pending = nil
	b.size = 0
	return out
}
