package helps

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/tidwall/gjson"
)

const MediaResultHeader = "X-CPA-Media-Result"
const MediaResultCompleted = "completed"
const MediaResultPending = "pending"
const MediaResultUnverified = "unverified"

// InspectMediaResult checks both the original envelope and the delivered output.
// Normalization must not turn a pending preview into a completed result by
// dropping its status fields. Inspection never fetches remote result URLs.
func InspectMediaResult(kind string, operation config.MediaOperation, envelope, body []byte, headers http.Header) (string, error) {
	state, err := inspectMediaOutput(kind, operation, body, headers)
	if err != nil || bytes.Equal(envelope, body) {
		return state, err
	}
	var value any
	if json.Unmarshal(envelope, &value) != nil {
		return state, nil
	}
	_, pending, err := inspectMediaJSON(value, kind, config.MediaOperationCapability(operation), state == MediaResultCompleted)
	if err != nil {
		return MediaResultUnverified, err
	}
	if pending && !(state == MediaResultCompleted && mediaAsyncResultCompleted(operation, envelope)) {
		return MediaResultPending, nil
	}
	return state, nil
}

func inspectMediaOutput(kind string, operation config.MediaOperation, body []byte, headers http.Header) (string, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return MediaResultUnverified, fmt.Errorf("media upstream returned an empty result")
	}
	capability := config.MediaOperationCapability(operation)
	var value any
	if json.Unmarshal(body, &value) == nil {
		completed, pending, err := inspectMediaJSON(value, kind, capability, false)
		if err != nil {
			return MediaResultUnverified, err
		}
		if kind == config.MediaKindAudio && capability == config.MediaCapabilityTranscribe {
			transcript := gjson.GetBytes(body, "choices.0.message.content")
			completed = completed || (gjson.GetBytes(body, "choices.0.finish_reason").String() == "stop" && transcript.Type == gjson.String && strings.TrimSpace(transcript.String()) != "")
		}
		if completed && mediaAsyncResultCompleted(operation, body) {
			return MediaResultCompleted, nil
		}
		if pending {
			return MediaResultPending, nil
		}
		if completed {
			return MediaResultCompleted, nil
		}
		return MediaResultUnverified, nil
	}
	if mediaBytesRecognized(kind, body, headers.Get("Content-Type")) {
		return MediaResultCompleted, nil
	}
	if kind == config.MediaKindAudio && capability == config.MediaCapabilityTranscribe && strings.HasPrefix(strings.ToLower(headers.Get("Content-Type")), "text/") {
		return MediaResultCompleted, nil
	}
	return MediaResultUnverified, nil
}

func mediaAsyncResultCompleted(operation config.MediaOperation, body []byte) bool {
	if operation.Async == nil {
		return false
	}
	status := strings.TrimSpace(gjson.GetBytes(body, operation.Async.StatusPath).String())
	for _, success := range operation.Async.SuccessValues {
		if strings.EqualFold(status, strings.TrimSpace(success)) {
			return true
		}
	}
	return false
}

func inspectMediaJSON(value any, kind, capability string, knownOutput bool) (completed, pending bool, err error) {
	switch v := value.(type) {
	case []any:
		for _, item := range v {
			c, p, e := inspectMediaJSON(item, kind, capability, knownOutput)
			if e != nil {
				return false, false, e
			}
			completed = completed || c
			pending = pending || p
		}
	case map[string]any:
		for _, field := range []string{"status", "state"} {
			if status, ok := v[field].(string); ok {
				switch strings.ToLower(strings.TrimSpace(status)) {
				case "failed", "failure", "error", "canceled", "cancelled":
					return false, false, fmt.Errorf("media upstream reported %s", status)
				case "pending", "queued", "processing", "in_progress", "running", "submitted":
					pending = true
				}
			}
		}
		for key, item := range v {
			text, isText := item.(string)
			if isText {
				switch key {
				case "url", "image_url", "audio_url", "video_url", "output_url":
					parsed, e := url.Parse(strings.TrimSpace(text))
					if e == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != "" {
						completed = true
					}
				case "b64_json":
					// URL responses may retain an empty optional base64 field.
					if strings.TrimSpace(text) == "" {
						continue
					}
					data, e := base64.StdEncoding.Strict().DecodeString(strings.TrimSpace(text))
					if e != nil || len(data) == 0 {
						return false, false, fmt.Errorf("media upstream returned invalid base64")
					}
					completed = completed || mediaBytesRecognized(kind, data, "")
				case "text", "transcript", "transcription":
					if kind == config.MediaKindAudio && capability == config.MediaCapabilityTranscribe && strings.TrimSpace(text) != "" {
						completed = true
					}
				case "id", "voice_id":
					if capability == config.MediaCapabilityClone && strings.TrimSpace(text) != "" {
						completed = true
					}
				}
			}
			// Only response containers are traversed; echoed requests and usage metadata
			// are not evidence that a media result exists.
			switch key {
			case "data", "result", "output", "outputs", "images", "videos", "audio", "choices", "message":
				c, p, e := inspectMediaJSON(item, kind, capability, knownOutput)
				if e != nil {
					return false, false, e
				}
				completed = completed || c
				pending = pending || p
			}
		}
		// A task ID without a result is only a receipt; alongside a completed
		// result it is provenance, unless an explicit pending status says otherwise.
		if _, hasTask := v["task_id"]; hasTask && !completed && !knownOutput {
			pending = true
		}
		// A completed task may still include its task ID as provenance.
		if completed {
			status, _ := v["status"].(string)
			switch strings.ToLower(status) {
			case "completed", "succeeded", "success", "done":
				pending = false
			}
		}
	}
	return
}

func mediaBytesRecognized(kind string, data []byte, contentType string) bool {
	if len(data) == 0 {
		return false
	}
	if kind == config.MediaKindImage {
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err == nil && cfg.Width > 0 && cfg.Height > 0 {
			return true
		}
		return len(data) > 20 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP"
	}
	if kind == config.MediaKindAudio {
		if len(data) > 44 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WAVE" {
			for offset := 12; offset+8 <= len(data); {
				size := int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
				if size > len(data)-offset-8 {
					return false
				}
				if string(data[offset:offset+4]) == "data" {
					return size > 0
				}
				offset += 8 + size + (size % 2)
			}
			return false
		}
		if len(data) > 16 && (bytes.HasPrefix(data, []byte("ID3")) || bytes.HasPrefix(data, []byte("OggS")) || bytes.HasPrefix(data, []byte("fLaC")) || (data[0] == 0xff && data[1]&0xe0 == 0xe0)) {
			return true
		}
		if strings.HasPrefix(strings.ToLower(contentType), "audio/pcm") && len(data)%2 == 0 {
			return true
		}
	}
	return len(data) > 16 && ((string(data[4:8]) == "ftyp") || bytes.HasPrefix(data, []byte{0x1a, 0x45, 0xdf, 0xa3}))
}
