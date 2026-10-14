package helps

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/mediarouting"
	"github.com/tidwall/gjson"
)

// MediaWireRequest contains a validated protocol conversion, not a second executor.
type MediaWireRequest struct {
	Payload               []byte
	ContentType           string
	RequestFormat         string
	OutputFormat          string
	RequireChatCompletion bool
}

type mediaAdapterError struct{ message string }

func (e mediaAdapterError) Error() string         { return e.message }
func (e mediaAdapterError) StatusCode() int       { return http.StatusBadRequest }
func (e mediaAdapterError) IsRequestScoped() bool { return true }
func invalidMediaInput(message string) error      { return mediaAdapterError{message: message} }

func AdaptMediaRequest(provider *config.MediaProvider, operation config.MediaOperation, model string, payload []byte, contentType string) (MediaWireRequest, error) {
	request := MediaWireRequest{Payload: payload, ContentType: contentType, RequestFormat: operation.RequestFormat}
	switch mediarouting.Adapter(provider) {
	case "passthrough":
		return request, nil
	case "cloudflare-workers-ai":
		return adaptCloudflareMediaRequest(operation, model, request)
	case "mimo-audio":
		return adaptMiMoMediaRequest(operation, model, request)
	default:
		return request, invalidMediaInput("unsupported media adapter")
	}
}

type mediaFormFile struct {
	Name, Filename, ContentType string
	Data                        []byte
}

func readMediaForm(payload []byte, contentType string) (map[string]string, []mediaFormFile, error) {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil || params["boundary"] == "" {
		return nil, nil, invalidMediaInput("media request requires multipart/form-data")
	}
	fields := map[string]string{}
	var files []mediaFormFile
	reader := multipart.NewReader(bytes.NewReader(payload), params["boundary"])
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, invalidMediaInput("invalid media multipart body")
		}
		data, err := io.ReadAll(part)
		if err != nil {
			return nil, nil, err
		}
		if part.FileName() != "" {
			files = append(files, mediaFormFile{Name: part.FormName(), Filename: part.FileName(), ContentType: part.Header.Get("Content-Type"), Data: data})
		} else {
			fields[part.FormName()] = string(data)
		}
	}
	return fields, files, nil
}

func writeMediaForm(fields map[string]string, files []mediaFormFile) ([]byte, string, error) {
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := writer.WriteField(key, fields[key]); err != nil {
			return nil, "", err
		}
	}
	for _, file := range files {
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": file.Name, "filename": file.Filename}))
		header.Set("Content-Type", file.ContentType)
		part, err := writer.CreatePart(header)
		if err != nil {
			return nil, "", err
		}
		if _, err = part.Write(file.Data); err != nil {
			return nil, "", err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return buffer.Bytes(), writer.FormDataContentType(), nil
}

// AdaptMediaResult restores the standard audio response shape only when the
// request used the standard input contract. Native messages remain native.
func AdaptMediaResult(request MediaWireRequest, body []byte, headers http.Header) ([]byte, bool, error) {
	if request.RequireChatCompletion && gjson.GetBytes(body, "choices.0.finish_reason").String() != "stop" {
		return nil, false, fmt.Errorf("MiMo returned an incomplete audio response")
	}
	if request.OutputFormat == "" {
		return body, false, nil
	}
	if strings.HasPrefix(request.OutputFormat, "audio/") {
		data, err := base64.StdEncoding.Strict().DecodeString(gjson.GetBytes(body, "choices.0.message.audio.data").String())
		if err != nil || len(data) == 0 {
			return nil, false, fmt.Errorf("MiMo returned no valid audio data")
		}
		headers.Set("Content-Type", request.OutputFormat)
		return data, true, nil
	}
	text := strings.TrimSpace(gjson.GetBytes(body, "choices.0.message.content").String())
	reason := gjson.GetBytes(body, "choices.0.finish_reason").String()
	if text == "" || reason != "stop" {
		return nil, false, fmt.Errorf("MiMo returned no completed transcription")
	}
	if request.OutputFormat == "text" {
		headers.Set("Content-Type", "text/plain; charset=utf-8")
		return []byte(text), true, nil
	}
	data, err := json.Marshal(map[string]string{"text": text})
	headers.Set("Content-Type", "application/json")
	return data, true, err
}
