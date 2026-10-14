package helps

import (
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func adaptMiMoMediaRequest(operation config.MediaOperation, model string, request MediaWireRequest) (MediaWireRequest, error) {
	capability := config.MediaOperationCapability(operation)
	if capability == config.MediaCapabilityTranscribe && model == "mimo-v2.5-asr" {
		request.RequireChatCompletion = true
		return adaptMiMoTranscription(request)
	}
	if !strings.HasPrefix(model, "mimo-") || !strings.Contains(model, "tts") {
		return request, nil
	}
	request.RequireChatCompletion = true
	var input map[string]any
	if err := json.Unmarshal(request.Payload, &input); err != nil || input == nil {
		return request, invalidMediaInput("MiMo speech input must be a JSON object")
	}
	if input["stream"] == true {
		return request, invalidMediaInput("media operations require stream=false")
	}
	if _, native := input["messages"]; native {
		return request, nil
	}
	text, ok := input["input"].(string)
	if !ok || strings.TrimSpace(text) == "" {
		return request, invalidMediaInput("MiMo speech requires input text or native messages")
	}
	if speed, ok := input["speed"]; ok && speed != float64(1) {
		return request, invalidMediaInput("MiMo speech uses instructions for speaking speed; numeric speed must be 1")
	}
	var messages []any
	instructions, _ := input["instructions"].(string)
	if strings.Contains(model, "voicedesign") && strings.TrimSpace(instructions) == "" {
		return request, invalidMediaInput("MiMo voice design requires instructions")
	}
	if instructions != "" {
		messages = append(messages, map[string]string{"role": "user", "content": instructions})
	}
	messages = append(messages, map[string]string{"role": "assistant", "content": text})
	audio, _ := input["audio"].(map[string]any)
	if audio == nil {
		audio = map[string]any{}
	}
	format, _ := input["response_format"].(string)
	if format == "" {
		format, _ = audio["format"].(string)
	}
	if format == "" {
		format = "mp3"
	}
	switch format {
	case "wav":
		request.OutputFormat = "audio/wav"
	case "mp3":
		request.OutputFormat = "audio/mpeg"
	case "pcm", "pcm16":
		request.OutputFormat = "audio/pcm"
	default:
		return request, invalidMediaInput("MiMo speech supports wav, mp3 or pcm audio")
	}
	audio["format"] = format
	if voice, ok := input["voice"]; ok {
		audio["voice"] = voice
	}
	input["messages"] = messages
	input["audio"] = audio
	for _, key := range []string{"input", "instructions", "voice", "response_format", "speed"} {
		delete(input, key)
	}
	var err error
	request.Payload, err = json.Marshal(input)
	request.ContentType = "application/json"
	request.RequestFormat = config.MediaRequestJSON
	return request, err
}

func adaptMiMoTranscription(request MediaWireRequest) (MediaWireRequest, error) {
	if strings.HasPrefix(strings.ToLower(request.ContentType), "application/json") {
		request.RequestFormat = config.MediaRequestJSON
		return request, nil
	}
	fields, files, err := readMediaForm(request.Payload, request.ContentType)
	if err != nil {
		return request, err
	}
	var file *mediaFormFile
	for i := range files {
		if files[i].Name == "file" {
			if file != nil {
				return request, invalidMediaInput("MiMo transcription requires exactly one audio file")
			}
			file = &files[i]
		}
	}
	if file == nil || len(file.Data) == 0 {
		return request, invalidMediaInput("MiMo transcription requires a nonempty file")
	}
	format := strings.ToLower(strings.TrimPrefix(filepath.Ext(file.Filename), "."))
	if format != "wav" && format != "mp3" {
		return request, invalidMediaInput("MiMo transcription supports WAV or MP3 input")
	}
	if base64.StdEncoding.EncodedLen(len(file.Data)) > 10<<20 {
		return request, invalidMediaInput("MiMo transcription encoded audio exceeds 10 MiB")
	}
	language := fields["language"]
	if language != "" && language != "auto" && language != "zh" && language != "en" {
		return request, invalidMediaInput("MiMo transcription language must be auto, zh or en")
	}
	request.OutputFormat = fields["response_format"]
	if request.OutputFormat == "" {
		request.OutputFormat = "json"
	}
	if request.OutputFormat != "json" && request.OutputFormat != "text" {
		return request, invalidMediaInput("MiMo transcription response_format must be json or text")
	}
	payload := map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_audio", "input_audio": map[string]string{"data": base64.StdEncoding.EncodeToString(file.Data), "format": format}}}}}}
	if language != "" {
		payload["asr_options"] = map[string]string{"language": language}
	}
	request.Payload, err = json.Marshal(payload)
	request.ContentType = "application/json"
	request.RequestFormat = config.MediaRequestJSON
	return request, err
}
