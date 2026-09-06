package management

import (
	"encoding/json"
	"strings"
)

// requestLogSSEPayloads reads complete SSE data frames. Older relays also emit
// consecutive JSON data lines without separators; keep those as separate events.
func requestLogSSEPayloads(text string) []string {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	var payloads []string
	var lines []string
	var event string
	var trailingEvent *string
	flush := func() {
		if trailingEvent != nil {
			event = *trailingEvent
		}
		if len(lines) > 0 {
			payload := strings.Join(lines, "\n")
			// The chunk logger can append an Error annotation without a newline.
			// Recover only this known log suffix, not arbitrary trailing data.
			if strings.Contains(payload, "Error:") {
				decoder := json.NewDecoder(strings.NewReader(payload))
				var raw json.RawMessage
				if decoder.Decode(&raw) == nil && strings.HasPrefix(strings.TrimSpace(payload[decoder.InputOffset():]), "Error:") {
					payload = string(raw)
				}
			}
			if event != "" {
				var object map[string]json.RawMessage
				if json.Unmarshal([]byte(payload), &object) == nil && object != nil && len(object["type"]) == 0 {
					object["type"], _ = json.Marshal(event)
					if encoded, err := json.Marshal(object); err == nil {
						payload = string(encoded)
					}
				}
			}
			payloads = append(payloads, payload)
		}
		lines = nil
		event = ""
		trailingEvent = nil
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			flush()
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch field {
		case "event":
			if len(lines) > 0 {
				trailingEvent = &value
			} else {
				event = value
			}
		case "data":
			if len(lines) > 0 && (strings.HasPrefix(value, "{") || value == "[DONE]") {
				previous := strings.Join(lines, "\n")
				if json.Valid([]byte(previous)) || previous == "[DONE]" {
					// A second complete JSON payload marks a legacy frame boundary.
					// An intervening event field belongs to that next payload;
					// otherwise a trailing event still belongs to the current frame.
					nextEvent := trailingEvent
					trailingEvent = nil
					flush()
					if nextEvent != nil {
						event = *nextEvent
					}
				}
			}
			lines = append(lines, value)
		}
	}
	flush()
	return payloads
}

func requestLogErrorValue(value any) string {
	switch typed := value.(type) {
	case map[string]any:
		for _, field := range []string{"message", "code", "type"} {
			if text := stringFromAny(typed[field]); text != "" {
				return text
			}
		}
	case string:
		if text := strings.TrimSpace(typed); text != "" {
			return text
		}
	}
	return "upstream error"
}

func requestLogObjectError(object map[string]any) string {
	if value := object["error"]; value != nil {
		return requestLogErrorValue(value)
	}
	if response, ok := object["response"].(map[string]any); ok {
		if value := response["error"]; value != nil {
			return requestLogErrorValue(value)
		}
		if strings.EqualFold(stringFromAny(response["status"]), "failed") {
			return "response.failed"
		}
	}
	switch strings.ToLower(stringFromAny(object["type"])) {
	case "error", "response.error", "response.failed":
		for _, field := range []string{"message", "code", "type"} {
			if text := stringFromAny(object[field]); text != "" {
				return text
			}
		}
	}
	if strings.EqualFold(stringFromAny(object["status"]), "failed") {
		return "response.failed"
	}
	return ""
}

func requestLogErrorDetails(body string) string {
	if strings.Contains(body, "\ndata:") || strings.HasPrefix(strings.TrimSpace(body), "data:") {
		var details []string
		for _, payload := range requestLogSSEPayloads(body) {
			if errorMessageFromJSON(payload) != "" {
				details = append(details, formatJSONForDisplay(payload))
			}
		}
		return strings.Join(uniqueStrings(details), "\n\n")
	}
	return formatJSONForDisplay(body)
}

// requestLogResponseSucceeded recognizes a final downstream result, so earlier
// failed credentials do not turn a successful retry into a failed request.
func requestLogResponseSucceeded(body string, status int) bool {
	if status < 200 || status >= 400 || extractErrorFromResponseBody(body) != "" {
		return false
	}
	isSSE := strings.Contains(body, "\ndata:") || strings.HasPrefix(strings.TrimSpace(body), "data:")
	payloads := []string{body}
	if isSSE {
		payloads = requestLogSSEPayloads(body)
	}
	for _, payload := range payloads {
		if payload == "[DONE]" {
			return true
		}
		var object map[string]any
		if json.Unmarshal([]byte(payload), &object) != nil {
			continue
		}
		switch stringFromAny(object["type"]) {
		case "response.completed", "response.done", "response.incomplete", "message_stop":
			return true
		}
		if wrapped, ok := object["response"].(map[string]any); ok {
			switch stringFromAny(wrapped["status"]) {
			case "completed", "incomplete":
				return true
			}
		}
		if !isSSE {
			switch stringFromAny(object["status"]) {
			case "completed", "incomplete":
				return true
			case "in_progress", "queued", "failed":
				continue
			}
			for _, field := range []string{"output", "choices", "content", "candidates", "data"} {
				if _, ok := object[field]; ok {
					return true
				}
			}
		}
		if choices, ok := object["choices"].([]any); ok {
			for _, choice := range choices {
				if item, ok := choice.(map[string]any); ok && stringFromAny(item["finish_reason"]) != "" {
					return true
				}
			}
		}
		if candidates, ok := object["candidates"].([]any); ok {
			for _, candidate := range candidates {
				if item, ok := candidate.(map[string]any); ok && stringFromAny(item["finishReason"]) != "" {
					return true
				}
			}
		}
	}
	return false
}

func requestLogResponseErrors(sections map[string][]string, response string, status int, logText string) (string, string) {
	body := responseBody(response)
	if message := extractErrorFromResponseBody(body); message != "" {
		return cleanDisplayText(message), cleanDisplayText(requestLogErrorDetails(body))
	}
	if requestLogResponseSucceeded(body, status) {
		return "", ""
	}
	if last := requestLogFinalAttemptResponse(logText); last != "" {
		upstreamBody := extractUpstreamBody(last)
		if upstreamBody == "" {
			upstreamBody = stripStatusAndHeaders(last)
		}
		if message := extractErrorFromResponseBody(upstreamBody); message != "" {
			return cleanDisplayText(message), cleanDisplayText(requestLogErrorDetails(upstreamBody))
		}
	}
	apiErrors := sections["API ERROR RESPONSE"]
	return extractErrorPreviewText(apiErrors, response, status), extractErrorFullText(apiErrors, response, status)
}

// Match explicit attempt numbers rather than slice positions. Legacy unnumbered
// responses are considered only when there is exactly one request and response.
func requestLogFinalAttemptResponse(text string) string {
	var current, lastRequest string
	var body strings.Builder
	var requestCount, responseCount int
	responses := make(map[string]string)
	flush := func() {
		if current == "API RESPONSE" || strings.HasPrefix(current, "API RESPONSE ") {
			responses[current] = body.String()
			responseCount++
		}
		body.Reset()
	}
	for _, line := range strings.Split(text, "\n") {
		if match := requestLogSectionHeader.FindStringSubmatch(strings.TrimSpace(line)); len(match) == 2 {
			flush()
			current = strings.TrimSpace(match[1])
			if current == "API REQUEST" || strings.HasPrefix(current, "API REQUEST ") {
				lastRequest = current
				requestCount++
			}
			continue
		}
		if current == "API RESPONSE" || strings.HasPrefix(current, "API RESPONSE ") {
			body.WriteString(line)
			body.WriteByte('\n')
		}
	}
	flush()
	if ordinal := strings.TrimSpace(strings.TrimPrefix(lastRequest, "API REQUEST")); ordinal != "" {
		if matched, ok := responses["API RESPONSE "+ordinal]; ok {
			return matched
		}
	}
	if requestCount == 1 && responseCount == 1 {
		if matched, ok := responses["API RESPONSE"]; ok {
			return matched
		}
		if lastRequest == "API REQUEST" {
			for _, matched := range responses {
				return matched
			}
		}
	}
	return ""
}

func builtInResponseToolName(typ string) string {
	switch typ {
	case "tool_search_call", "web_search_call", "file_search_call", "image_generation_call",
		"code_interpreter_call", "computer_call", "mcp_call", "local_shell_call", "shell_call":
		return strings.TrimSuffix(typ, "_call")
	case "function_call", "custom_tool_call", "tool_use", "server_tool_use":
		return typ
	default:
		return ""
	}
}

// Delta whitespace is significant: trim only the fully assembled answer.
func responseDeltaText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		var out strings.Builder
		for _, item := range typed {
			out.WriteString(responseDeltaText(item))
		}
		return out.String()
	case map[string]any:
		typ := strings.ToLower(stringFromAny(typed["type"]))
		if typ == "" || typ == "text" || typ == "output_text" || typ == "input_text" {
			if text, ok := typed["text"].(string); ok {
				return text
			}
			return responseDeltaText(typed["parts"])
		}
	}
	return ""
}
