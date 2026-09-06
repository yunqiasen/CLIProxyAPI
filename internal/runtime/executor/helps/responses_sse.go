package helps

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// ResponsesSSEFrame retains the upstream representation separately from joined data.
type ResponsesSSEFrame struct {
	Data, Raw []byte
	Event     string
}

// ResponsesSSEReader reads bounded SSE frames, including legacy one-JSON-per-line streams.
type ResponsesSSEReader struct {
	scanner *bufio.Scanner
	limit   int
	pending []byte
	ended   bool
}

func NewResponsesSSEReader(r io.Reader, limit int) *ResponsesSSEReader {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, min(4096, limit)), limit)
	return &ResponsesSSEReader{scanner: scanner, limit: limit}
}

func (r *ResponsesSSEReader) Next() (ResponsesSSEFrame, error) {
	var f ResponsesSSEFrame
	for {
		var line []byte
		if r.pending != nil {
			line = r.pending
			r.pending = nil
		} else if !r.ended && r.scanner.Scan() {
			line = bytes.Clone(r.scanner.Bytes())
		} else {
			r.ended = true
			if err := r.scanner.Err(); err != nil && !json.Valid(f.Data) {
				return f, err
			}
			if len(f.Raw) > 0 {
				return f, nil
			}
			if err := r.scanner.Err(); err != nil {
				return f, err
			}
			return f, io.EOF
		}
		field, value, hasColon := bytes.Cut(line, []byte(":"))
		if hasColon && len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}
		// Some supported relays omit blank separators between complete JSON events.
		// Split only complete JSON records, never a partial JSON field/string.
		if len(f.Data) > 0 && string(field) == "data" && json.Valid(f.Data) {
			r.pending = line
			return f, nil
		}
		if len(f.Raw)+len(line)+1 > r.limit {
			return f, fmt.Errorf("upstream SSE frame exceeds %d bytes", r.limit)
		}
		f.Raw = append(f.Raw, line...)
		f.Raw = append(f.Raw, '\n')
		if len(line) == 0 {
			if len(f.Data) > 0 || f.Event != "" || len(bytes.TrimSpace(f.Raw)) > 0 {
				return f, nil
			}
			f.Raw = nil
			continue
		}
		if !hasColon {
			continue
		}
		switch string(field) {
		case "event":
			f.Event = string(value)
		case "data":
			if f.Data != nil {
				f.Data = append(f.Data, '\n')
			}
			f.Data = append(f.Data, value...)
		}
	}
}
