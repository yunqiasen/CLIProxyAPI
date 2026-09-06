package util

import (
	"bytes"
	"encoding/json"
)

// SSEChunkNeedsLineBreak joins legacy line-sized chunks without splitting a
// partial field name or treating JSON string content as a new SSE field.
func SSEChunkNeedsLineBreak(pending, chunk []byte) bool {
	if len(pending) == 0 || len(chunk) == 0 ||
		pending[len(pending)-1] == '\n' || pending[len(pending)-1] == '\r' ||
		chunk[0] == '\n' || chunk[0] == '\r' {
		return false
	}
	first := bytes.TrimLeft(chunk, " \t")
	newField := false
	for _, prefix := range []string{"data:", "event:", "id:", "retry:", ":"} {
		if bytes.HasPrefix(first, []byte(prefix)) {
			newField = true
			break
		}
	}
	if !newField {
		return false
	}

	lastLine := bytes.TrimSpace(pending[bytes.LastIndexByte(pending, '\n')+1:])
	field, value, found := bytes.Cut(lastLine, []byte(":"))
	if !found || len(bytes.TrimSpace(value)) == 0 {
		return false
	}
	switch string(field) {
	case "event", "id", "retry", "":
		return true
	case "data":
		// Quotes and escapes can straddle chunks; examine all data lines in
		// the pending frame before deciding that a missing newline is real.
		var payload []byte
		for _, line := range bytes.Split(pending, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}
			payload = append(payload, line[len("data:"):]...)
			payload = append(payload, '\n')
		}
		if first[0] == ':' {
			// A colon inside an unfinished object is a JSON key separator,
			// not a comment line emitted by a legacy executor.
			return json.Valid(payload)
		}
		inString, escaped := false, false
		for _, b := range payload {
			if escaped {
				escaped = false
				continue
			}
			if inString && b == '\\' {
				escaped = true
			} else if b == '"' {
				inString = !inString
			}
		}
		return !inString
	default:
		return false
	}
}
