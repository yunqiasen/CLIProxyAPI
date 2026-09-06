package management

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// requestLogWebsocketExchange projects the last client turn into the existing
// request/response parser. Raw session transcripts remain untouched.
func requestLogWebsocketExchange(timeline string) (string, string) {
	var request, model string
	var response strings.Builder
	status := http.StatusRequestTimeout
	terminal := false
	for timeline != "" {
		_, remaining, ok := strings.Cut(timeline, "Event: websocket.")
		if !ok {
			break
		}
		direction, remaining, ok := strings.Cut(remaining, "\n")
		if !ok {
			break
		}
		decoder := json.NewDecoder(strings.NewReader(remaining))
		var rawFrame json.RawMessage
		if decoder.Decode(&rawFrame) != nil {
			timeline = remaining
			continue
		}
		timeline = remaining[decoder.InputOffset():]
		if !gjson.ParseBytes(rawFrame).IsObject() {
			continue
		}
		switch strings.TrimSpace(direction) {
		case "request":
			if next := gjson.GetBytes(rawFrame, "model").String(); next != "" {
				model = next
			}
			if model != "" && gjson.GetBytes(rawFrame, "model").String() == "" {
				rawFrame, _ = sjson.SetBytes(rawFrame, "model", model)
			}
			request = string(rawFrame)
			response.Reset()
			status = http.StatusRequestTimeout
			terminal = false
		case "response", "disconnect":
			if request == "" || terminal {
				continue
			}
			kind := gjson.GetBytes(rawFrame, "type").String()
			switch kind {
			case "response.completed", "response.done":
				status = http.StatusOK
				terminal = true
			case "error", "response.failed":
				status = int(gjson.GetBytes(rawFrame, "status").Int())
				if status < 400 || status > 599 {
					status = http.StatusBadGateway
				}
				terminal = true
			}
			if strings.TrimSpace(direction) == "disconnect" {
				status = http.StatusBadGateway
				terminal = true
				if kind == "" {
					rawFrame, _ = sjson.SetBytes(rawFrame, "type", "error")
				}
			}
			var compact bytes.Buffer
			if json.Compact(&compact, rawFrame) != nil {
				continue
			}
			response.WriteString("data: ")
			response.WriteString(compact.String())
			response.WriteString("\n\n")
		}
	}
	if request == "" {
		return "", ""
	}
	return request, fmt.Sprintf("Status: %d\n\n%s", status, response.String())
}
