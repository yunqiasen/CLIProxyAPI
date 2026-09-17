package helps

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// CodexResponsesSSEReader repairs only Agent's complete forced-function EOFs.
// Raw always retains the upstream bytes; adapter-created events have no Raw bytes.
// Both production execution modes and management probes use this reader.
type CodexResponsesSSEReader struct {
	reader  *ResponsesSSEReader
	ctx     context.Context
	repair  *agentToolTerminal
	pending *ResponsesSSEFrame
	ended   bool
}

func NewCodexResponsesSSEReader(ctx context.Context, body io.Reader, limit int, endpoint string, request []byte) *CodexResponsesSSEReader {
	return &CodexResponsesSSEReader{reader: NewResponsesSSEReader(body, limit), ctx: ctx, repair: newAgentToolTerminal(endpoint, request, limit)}
}

func (r *CodexResponsesSSEReader) Next() (ResponsesSSEFrame, error) {
	if r.pending != nil {
		frame := *r.pending
		r.pending = nil
		return frame, nil
	}
	frame, err := r.reader.Next()
	if err != nil {
		if err == io.EOF && !r.ended && (r.ctx == nil || r.ctx.Err() == nil) && r.repair != nil {
			r.ended = true
			if completed := r.repair.completed(); completed != nil {
				LogWithRequestID(r.ctx).Debug("responses compatibility: completed Agent forced-function EOF from verified tool items")
				return ResponsesSSEFrame{Data: completed}, nil
			}
		}
		return frame, err
	}
	if r.repair != nil {
		if created := r.repair.observe(&frame); created != nil {
			LogWithRequestID(r.ctx).Debug("responses compatibility: assigned local identity to Agent forced-function stream without response lifecycle")
			r.pending = &frame
			return ResponsesSSEFrame{Data: created}, nil
		}
	}
	return frame, nil
}

type agentToolItem struct {
	id, callID, name string
	deltas           strings.Builder
	arguments        string
	argumentsDone    bool
	output           json.RawMessage
}

type agentToolTerminal struct {
	name, model string
	limit, size int
	invalid     bool
	terminal    bool
	localID     bool
	sequence    int64
	response    map[string]any
	items       map[int64]*agentToolItem
}

func newAgentToolTerminal(endpoint string, request []byte, limit int) *agentToolTerminal {
	parsed, err := url.Parse(endpoint)
	if err != nil || !strings.EqualFold(strings.TrimSuffix(parsed.Hostname(), "."), "agentrouter.org") ||
		!strings.HasSuffix(strings.TrimSuffix(parsed.Path, "/"), "/responses") ||
		gjson.GetBytes(request, "model").String() != "gpt-6-astra" ||
		gjson.GetBytes(request, "tool_choice.type").String() != "function" ||
		gjson.GetBytes(request, "store").Bool() || gjson.GetBytes(request, "previous_response_id").String() != "" {
		return nil
	}
	name := gjson.GetBytes(request, "tool_choice.name").String()
	if strings.TrimSpace(name) == "" {
		return nil
	}
	found := false
	for _, tool := range gjson.GetBytes(request, "tools").Array() {
		found = found || tool.Get("type").String() == "function" && tool.Get("name").String() == name
	}
	if !found {
		return nil
	}
	for _, item := range gjson.GetBytes(request, "input").Array() {
		switch item.Get("type").String() {
		case "compaction", "compaction_summary", "compaction_trigger", "item_reference":
			return nil
		}
	}
	return &agentToolTerminal{name: name, model: gjson.GetBytes(request, "model").String(), limit: limit, sequence: -1, items: make(map[int64]*agentToolItem)}
}

func (s *agentToolTerminal) observe(frame *ResponsesSSEFrame) []byte {
	data := bytes.TrimSpace(frame.Data)
	if len(data) == 0 {
		if frame.Event != "" {
			s.invalid = true
		}
		return nil
	}
	if bytes.Equal(data, []byte("[DONE]")) {
		if frame.Event != "" {
			s.invalid = true
		}
		return nil
	}
	if !json.Valid(data) {
		s.invalid = true
		return nil
	}
	root := gjson.ParseBytes(data)
	event := root.Get("type").String()
	if event == "" {
		event = frame.Event
	} else if frame.Event != "" && frame.Event != event {
		s.invalid = true
	}
	if event == "response.completed" || event == "response.done" || event == "response.incomplete" || event == "response.failed" || event == "response.cancelled" || event == "response.canceled" || event == "error" {
		s.terminal = true
	}
	if root.Get("error").Type != gjson.Null || root.Get("response.error").Type != gjson.Null || root.Get("response.incomplete_details").Type != gjson.Null {
		s.invalid = true
	}
	var created []byte
	if !s.invalid && !s.terminal {
		s.size += len(data)
		if s.size > s.limit || !s.observeEvent(root, event) {
			s.invalid = true
			s.items = nil
		} else if s.response == nil && event == "response.output_item.added" {
			s.localID = true
			s.response = map[string]any{"id": "resp_cpa_" + strings.ReplaceAll(uuid.NewString(), "-", ""), "object": "response", "model": s.model, "created_at": time.Now().Unix(), "status": "in_progress", "output": []any{}, "error": nil, "incomplete_details": nil, "usage": nil, "store": false}
			created = s.event("response.created", s.response)
		}
	}
	if s.localID {
		// A route with no native lifecycle needs one stable client-visible identity.
		// Keep native raw logs intact even if a later native terminal does arrive.
		s.sequence++
		frame.Data, _ = sjson.SetBytes(data, "sequence_number", s.sequence)
		if root.Get("response").IsObject() {
			frame.Data, _ = sjson.SetBytes(frame.Data, "response.id", s.response["id"])
		}
	} else if seq := root.Get("sequence_number"); seq.Exists() && seq.Int() > s.sequence {
		s.sequence = seq.Int()
	}
	return created
}

func (s *agentToolTerminal) observeEvent(root gjson.Result, event string) bool {
	switch event {
	case "response.created", "response.in_progress", "response.queued":
		response := root.Get("response")
		status := response.Get("status").String()
		if len(s.items) != 0 || !response.IsObject() || response.Get("id").Type != gjson.String || strings.TrimSpace(response.Get("id").String()) == "" ||
			(status != "in_progress" && status != "queued") || !response.Get("output").IsArray() || len(response.Get("output").Array()) != 0 {
			return false
		}
		if s.response != nil && s.response["id"] != response.Get("id").String() {
			return false
		}
		return json.Unmarshal([]byte(response.Raw), &s.response) == nil
	case "response.output_item.added", "response.function_call_arguments.delta", "response.function_call_arguments.done", "response.output_item.done":
	default:
		return false
	}
	index := root.Get("output_index")
	if index.Type != gjson.Number || index.Int() < 0 || index.Float() != float64(index.Int()) {
		return false
	}
	i := index.Int()
	if event == "response.output_item.added" {
		item := root.Get("item")
		if s.items[i] != nil || item.Get("type").String() != "function_call" || item.Get("name").String() != s.name ||
			item.Get("id").Type != gjson.String || strings.TrimSpace(item.Get("id").String()) == "" ||
			item.Get("call_id").Type != gjson.String || strings.TrimSpace(item.Get("call_id").String()) == "" ||
			item.Get("arguments").Type != gjson.String || item.Get("arguments").String() != "" || item.Get("status").String() != "in_progress" {
			return false
		}
		for _, old := range s.items {
			if old.id == item.Get("id").String() || old.callID == item.Get("call_id").String() {
				return false
			}
		}
		s.items[i] = &agentToolItem{id: item.Get("id").String(), callID: item.Get("call_id").String(), name: s.name}
		return true
	}
	item := s.items[i]
	if item == nil || item.output != nil {
		return false
	}
	if event == "response.output_item.done" {
		done := root.Get("item")
		if !item.argumentsDone || done.Get("type").String() != "function_call" || done.Get("status").String() != "completed" ||
			done.Get("id").String() != item.id || done.Get("call_id").String() != item.callID || done.Get("name").String() != item.name ||
			done.Get("arguments").Type != gjson.String || done.Get("arguments").String() != item.arguments {
			return false
		}
		item.output = json.RawMessage(done.Raw)
		return true
	}
	if root.Get("item_id").String() != item.id || item.argumentsDone {
		return false
	}
	if event == "response.function_call_arguments.delta" {
		delta := root.Get("delta")
		if delta.Type != gjson.String {
			return false
		}
		item.deltas.WriteString(delta.String())
		return true
	}
	arguments := root.Get("arguments")
	if arguments.Type != gjson.String || !json.Valid([]byte(arguments.String())) || !gjson.Parse(arguments.String()).IsObject() ||
		(item.deltas.Len() != 0 && item.deltas.String() != arguments.String()) {
		return false
	}
	item.arguments, item.argumentsDone = arguments.String(), true
	return true
}

func (s *agentToolTerminal) completed() []byte {
	if s.invalid || s.terminal || len(s.items) == 0 || s.response == nil {
		return nil
	}
	output := make([]json.RawMessage, len(s.items))
	for i := range output {
		item := s.items[int64(i)]
		if item == nil || !item.argumentsDone || item.output == nil {
			return nil
		}
		output[i] = item.output
	}
	s.terminal = true
	s.response["status"], s.response["output"] = "completed", output
	s.response["completed_at"], s.response["error"], s.response["incomplete_details"] = time.Now().Unix(), nil, nil
	if _, ok := s.response["usage"]; !ok {
		s.response["usage"] = nil
	}
	return s.event("response.completed", s.response)
}

func (s *agentToolTerminal) event(kind string, response map[string]any) []byte {
	s.sequence++
	data, _ := json.Marshal(map[string]any{"type": kind, "sequence_number": s.sequence, "response": response})
	return data
}
