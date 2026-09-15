package responses

import (
	"bytes"
	"strings"

	common "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// restoreGeminiCustomEvents buffers function JSON deltas until the complete string
// input is known, then emits one lossless custom delta and its matching done event.
func restoreGeminiCustomEvents(events [][]byte, names map[string]common.ResponsesCustomTool, st *geminiToResponsesState) [][]byte {
	if len(names) == 0 {
		return events
	}
	if st.CustomCalls == nil {
		st.CustomCalls = map[string]bool{}
	}
	var result [][]byte
	emit := func(kind string, data []byte) {
		st.CustomSequence++
		data, _ = sjson.SetBytes(data, "sequence_number", st.CustomSequence)
		result = append(result, emitEvent(kind, data))
	}
	for _, event := range events {
		marker := bytes.Index(event, []byte("data: "))
		if marker < 0 {
			result = append(result, event)
			continue
		}
		data := bytes.TrimSpace(event[marker+6:])
		kind := gjson.GetBytes(data, "type").String()
		id := gjson.GetBytes(data, "item_id").String()
		switch kind {
		case "response.output_item.added", "response.output_item.done":
			item := gjson.GetBytes(data, "item")
			converted := common.RestoreResponsesCustomItem([]byte(item.Raw), names)
			if gjson.GetBytes(converted, "type").String() == "custom_tool_call" {
				st.CustomCalls[item.Get("id").String()] = true
				data, _ = sjson.SetRawBytes(data, "item", converted)
			}
		case "response.function_call_arguments.delta":
			if st.CustomCalls[id] {
				continue
			}
		case "response.function_call_arguments.done":
			if st.CustomCalls[id] {
				input := gjson.Get(gjson.GetBytes(data, "arguments").String(), "input").String()
				data, _ = sjson.DeleteBytes(data, "arguments")
				delta, _ := sjson.SetBytes(data, "type", "response.custom_tool_call_input.delta")
				delta, _ = sjson.SetBytes(delta, "delta", input)
				emit("response.custom_tool_call_input.delta", delta)
				kind = "response.custom_tool_call_input.done"
				data, _ = sjson.SetBytes(data, "type", kind)
				data, _ = sjson.SetBytes(data, "input", input)
			}
		default:
			if strings.HasPrefix(kind, "response.") {
				data = common.RestoreResponsesCustomOutput(data, names, "response.output")
			}
		}
		emit(kind, data)
	}
	return result
}
