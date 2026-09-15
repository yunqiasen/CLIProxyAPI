package common

import (
	"encoding/json"
	"strconv"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ResponsesCustomTool records the client-visible identity before function bridging.
type ResponsesCustomTool struct{ Name, Namespace string }

// ResponsesCustomToolNames returns the original freeform tool identities.
func ResponsesCustomToolNames(raw []byte) map[string]ResponsesCustomTool {
	names := map[string]ResponsesCustomTool{}
	for _, tool := range gjson.GetBytes(raw, "tools").Array() {
		if tool.Get("type").String() == "custom" {
			names[tool.Get("name").String()] = ResponsesCustomTool{Name: tool.Get("name").String()}
		}
		if tool.Get("type").String() == "namespace" {
			for _, child := range tool.Get("tools").Array() {
				if child.Get("type").String() == "custom" {
					names[tool.Get("name").String()+"."+child.Get("name").String()] = ResponsesCustomTool{Name: child.Get("name").String(), Namespace: tool.Get("name").String()}
				}
			}
		}
	}
	return names
}

// BridgeResponsesCustomTools represents freeform input as a single string
// argument for function-only backends. It only modifies a forwarded copy.
func BridgeResponsesCustomTools(raw []byte) []byte {
	tools := gjson.GetBytes(raw, "tools")
	if tools.IsArray() {
		converted := make([]json.RawMessage, 0, len(tools.Array()))
		var appendTool func(gjson.Result, string)
		appendTool = func(tool gjson.Result, prefix string) {
			if tool.Get("type").String() == "namespace" {
				for _, child := range tool.Get("tools").Array() {
					if child.Get("type").String() == "custom" {
						appendTool(child, tool.Get("name").String()+".")
					}
				}
				// Leave unrelated namespace declarations unchanged.
				converted = append(converted, json.RawMessage(tool.Raw))
				return
			}
			out := []byte(tool.Raw)
			if prefix != "" {
				out, _ = sjson.SetBytes(out, "name", prefix+tool.Get("name").String())
			}
			if tool.Get("type").String() == "custom" {
				out, _ = sjson.SetBytes(out, "type", "function")
				out, _ = sjson.SetRawBytes(out, "parameters", []byte(`{"type":"object","properties":{"input":{"type":"string"}},"required":["input"],"additionalProperties":false}`))
				out, _ = sjson.DeleteBytes(out, "format")
			}
			converted = append(converted, out)
		}
		for _, tool := range tools.Array() {
			appendTool(tool, "")
		}
		if out, err := json.Marshal(converted); err == nil {
			raw, _ = sjson.SetRawBytes(raw, "tools", out)
		}
	}
	input := gjson.GetBytes(raw, "input")
	for i, item := range input.Array() {
		path := "input." + strconv.Itoa(i)
		switch item.Get("type").String() {
		case "custom_tool_call":
			raw, _ = sjson.SetBytes(raw, path+".type", "function_call")
			args, _ := json.Marshal(map[string]string{"input": item.Get("input").String()})
			raw, _ = sjson.SetBytes(raw, path+".arguments", string(args))
			raw, _ = sjson.DeleteBytes(raw, path+".input")
			if namespace := item.Get("namespace").String(); namespace != "" {
				raw, _ = sjson.SetBytes(raw, path+".name", namespace+"."+item.Get("name").String())
			}
		case "custom_tool_call_output":
			raw, _ = sjson.SetBytes(raw, path+".type", "function_call_output")
		}
	}
	if gjson.GetBytes(raw, "tool_choice.type").String() == "custom" {
		raw, _ = sjson.SetBytes(raw, "tool_choice.type", "function")
	}
	return raw
}

func RestoreResponsesCustomItem(item []byte, names map[string]ResponsesCustomTool) []byte {
	if gjson.GetBytes(item, "type").String() != "function_call" {
		return item
	}
	name := gjson.GetBytes(item, "name").String()
	namespace := gjson.GetBytes(item, "namespace").String()
	qualified := name
	if namespace != "" {
		qualified = namespace + "." + name
	}
	identity, ok := names[qualified]
	if !ok && namespace == "" {
		identity, ok = names[name]
	}
	if !ok {
		return item
	}
	input := gjson.Get(gjson.GetBytes(item, "arguments").String(), "input").String()
	item, _ = sjson.SetBytes(item, "type", "custom_tool_call")
	item, _ = sjson.SetBytes(item, "input", input)
	item, _ = sjson.DeleteBytes(item, "arguments")
	item, _ = sjson.SetBytes(item, "name", identity.Name)
	if identity.Namespace != "" {
		item, _ = sjson.SetBytes(item, "namespace", identity.Namespace)
	}

	return item
}

func RestoreResponsesCustomOutput(raw []byte, names map[string]ResponsesCustomTool, path string) []byte {
	for i, item := range gjson.GetBytes(raw, path).Array() {
		raw, _ = sjson.SetRawBytes(raw, path+"."+strconv.Itoa(i), RestoreResponsesCustomItem([]byte(item.Raw), names))
	}
	return raw
}
