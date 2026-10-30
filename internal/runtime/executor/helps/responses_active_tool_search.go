package helps

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// retainActiveResponsesTools carries the callable part of client discovery when
// its input records are made portable. Only discoveries in the current user turn
// are loaded; older turns remain inert history. No catalog is cached or shared.
func retainActiveResponsesTools(body []byte, items []gjson.Result, records map[int]string) []byte {
	if len(records) == 0 {
		return body
	}
	tools := gjson.GetBytes(body, "tools")
	if tools.Exists() && !tools.IsArray() {
		return body
	}
	lastUser := -1
	for i, item := range items {
		if item.Get("role").String() == "user" {
			lastUser = i
		}
	}
	calls := make(map[string]bool)
	var outputs [][]gjson.Result
	for i := lastUser + 1; i < len(items); i++ {
		if _, ok := records[i]; !ok {
			continue
		}
		item := items[i]
		switch item.Get("type").String() {
		case "tool_search_call":
			calls[item.Get("call_id").String()] = true
		case "tool_search_output":
			if calls[item.Get("call_id").String()] {
				outputs = append(outputs, item.Get("tools").Array())
			}
		}
	}
	if len(outputs) == 0 {
		return body
	}
	var loaded []gjson.Result
	// Prefer the latest discovered definition when a client refreshes a tool.
	for i := len(outputs) - 1; i >= 0; i-- {
		loaded = append(loaded, outputs[i]...)
	}
	merged := mergeResponsesDiscoveredTools(tools.Array(), loaded, true)
	if len(merged) == 0 {
		return body
	}
	encoded, err := json.Marshal(merged)
	if err != nil {
		return body
	}
	// Preserve byte-for-byte identity when the catalog was already callable.
	if string(encoded) == tools.Raw {
		return body
	}
	updated, err := sjson.SetRawBytes(body, "tools", encoded)
	if err != nil {
		return body
	}
	return updated
}

// Explicit current definitions win over discovery metadata with the same name.
// Namespace members are merged without flattening names or dropping siblings.
func mergeResponsesDiscoveredTools(current, loaded []gjson.Result, namespaces bool) []json.RawMessage {
	out := make([]json.RawMessage, len(current))
	for i, tool := range current {
		out[i] = json.RawMessage(tool.Raw)
	}
	for _, tool := range loaded {
		typ, name := tool.Get("type").String(), tool.Get("name").String()
		if strings.TrimSpace(name) == "" || (typ != "function" && (typ != "namespace" || !namespaces)) {
			continue
		}
		if typ == "function" && tool.Get("parameters").Exists() && !tool.Get("parameters").IsObject() {
			continue
		}
		if typ == "namespace" && !tool.Get("tools").IsArray() {
			continue
		}
		index := -1
		for i, raw := range out {
			prior := gjson.ParseBytes(raw)
			if prior.Get("name").String() == name {
				index = i
				break
			}
		}
		selected := tool
		if index >= 0 {
			selected = gjson.ParseBytes(out[index])
			if selected.Get("type").String() != typ {
				continue
			}
		}
		raw := selected.Raw
		var err error
		if typ == "namespace" {
			if !selected.Get("tools").IsArray() {
				continue
			}
			var members []gjson.Result
			if index >= 0 {
				members = selected.Get("tools").Array()
			}
			merged := mergeResponsesDiscoveredTools(members, tool.Get("tools").Array(), false)
			if len(merged) == 0 {
				continue
			}
			encoded, errMarshal := json.Marshal(merged)
			if errMarshal != nil {
				continue
			}
			raw, err = sjson.SetRaw(raw, "tools", string(encoded))
		} else {
			raw, err = sjson.Delete(raw, "defer_loading")
		}
		if err != nil {
			continue
		}
		if index >= 0 {
			out[index] = json.RawMessage(raw)
		} else {
			out = append(out, json.RawMessage(raw))
		}
	}
	return out
}
