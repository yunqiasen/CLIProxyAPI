package helps

import (
	"bytes"

	"github.com/tidwall/gjson"
)

// ResponsesStreamBootstrap withholds lifecycle and empty scaffolding until output commits.
// A bounded prefix preserves liveness for relays that send only heartbeats.
type ResponsesStreamBootstrap struct {
	pending      [][]byte
	size, frames int
	committed    bool
}

func ResponsesLifecycleEvent(event string) bool {
	return event == "" || event == "keepalive" || event == "response.created" || event == "response.in_progress" || event == "response.queued"
}

// ResponsesProvisionalEvent identifies metadata before reasoning, text or tool work.
func ResponsesProvisionalEvent(event string, data []byte) bool {
	return ResponsesLifecycleEvent(event) || (event == "response.output_item.added" && responsesEmptyReasoningScaffold(data))
}

func (b *ResponsesStreamBootstrap) Committed() bool { return b.committed }
func (b *ResponsesStreamBootstrap) Reset()          { *b = ResponsesStreamBootstrap{} }
func (b *ResponsesStreamBootstrap) Push(event string, data []byte, chunks [][]byte) [][]byte {
	if b.committed {
		return chunks
	}
	b.frames++
	for _, chunk := range chunks {
		b.pending = append(b.pending, bytes.Clone(chunk))
		b.size += len(chunk)
	}
	if ResponsesProvisionalEvent(event, data) && b.frames < 16 && b.size < 64*1024 {
		return nil
	}
	b.committed = true
	out := b.pending
	b.pending = nil
	b.size = 0
	return out
}

// An empty reasoning item announces a slot, not generated reasoning or tool work.
// Its opaque state is provisional too; a summary delta or item completion commits it.
// Preserve it on success, but do not let it prevent pre-output credential recovery.
func responsesEmptyReasoningScaffold(data []byte) bool {
	item := gjson.GetBytes(data, "item")
	if item.Get("type").String() != "reasoning" {
		return false
	}
	for _, field := range []string{"summary", "content"} {
		value := item.Get(field)
		if value.Exists() && (!value.IsArray() || len(value.Array()) > 0) {
			return false
		}
	}
	return true
}
