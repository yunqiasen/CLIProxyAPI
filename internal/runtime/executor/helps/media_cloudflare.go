package helps

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func adaptCloudflareMediaRequest(operation config.MediaOperation, model string, request MediaWireRequest) (MediaWireRequest, error) {
	schnell := model == "@cf/black-forest-labs/flux-1-schnell"
	klein := strings.HasPrefix(model, "@cf/black-forest-labs/flux-2-klein-")
	if !schnell && !klein {
		return request, nil
	}
	if strings.HasPrefix(strings.ToLower(request.ContentType), "multipart/") {
		if klein {
			return adaptCloudflareKleinForm(operation, request)
		}
		return request, nil
	}
	var input map[string]json.RawMessage
	if err := json.Unmarshal(request.Payload, &input); err != nil || input == nil {
		return request, invalidMediaInput("Cloudflare image input must be a JSON object")
	}
	if n, ok := input["n"]; ok {
		var count int
		if json.Unmarshal(n, &count) != nil || count != 1 {
			return request, invalidMediaInput("Cloudflare image requests require n=1")
		}
		delete(input, "n")
	}
	if _, ok := input["steps"]; !ok {
		if steps, exists := input["num_steps"]; exists {
			input["steps"] = steps
		}
	}
	delete(input, "num_steps")
	if schnell {
		// Schnell has a fixed resolution and supports only prompt and steps.
		if size, ok := input["size"]; ok {
			var value string
			_ = json.Unmarshal(size, &value)
			if value != "1024x1024" && value != "auto" {
				return request, invalidMediaInput("Cloudflare Schnell supports only size 1024x1024")
			}
			delete(input, "size")
		}
		delete(input, "response_format")
		var err error
		request.Payload, err = json.Marshal(input)
		return request, err
	}
	fields := map[string]string{}
	for key, raw := range input {
		if key == "model" || key == "response_format" {
			continue
		}
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return request, err
		}
		switch v := value.(type) {
		case string:
			if key == "size" {
				parts := strings.Split(v, "x")
				if len(parts) != 2 {
					return request, invalidMediaInput("Cloudflare Klein size must be WIDTHxHEIGHT")
				}
				for _, part := range parts {
					if n, err := strconv.Atoi(part); err != nil || n <= 0 {
						return request, invalidMediaInput("invalid Cloudflare Klein size")
					}
				}
				fields["width"] = parts[0]
				fields["height"] = parts[1]
			} else {
				fields[key] = v
			}
		case float64, bool:
			fields[key] = fmt.Sprint(v)
		default:
			return request, invalidMediaInput("Cloudflare Klein JSON fields must be scalar; upload reference images as multipart")
		}
	}
	var err error
	request.Payload, request.ContentType, err = writeMediaForm(fields, nil)
	request.RequestFormat = config.MediaRequestMultipart
	if err != nil {
		return request, err
	}
	return adaptCloudflareKleinForm(operation, request)
}

// Klein uses binary input_image_N parts, not the OpenAI image field. The
// first-party contract is documented in the 2026-01-15 Workers AI changelog.
func adaptCloudflareKleinForm(operation config.MediaOperation, request MediaWireRequest) (MediaWireRequest, error) {
	fields, files, err := readMediaForm(request.Payload, request.ContentType)
	if err != nil {
		return request, err
	}
	if n, ok := fields["n"]; ok && n != "1" {
		return request, invalidMediaInput("Cloudflare image requests require n=1")
	}
	for _, key := range []string{"model", "n", "response_format"} {
		delete(fields, key)
	}
	if size, ok := fields["size"]; ok {
		parts := strings.Split(size, "x")
		if len(parts) != 2 {
			return request, invalidMediaInput("Cloudflare Klein size must be WIDTHxHEIGHT")
		}
		for _, value := range parts {
			if number, err := strconv.Atoi(value); err != nil || number < 256 || number > 1920 {
				return request, invalidMediaInput("Cloudflare Klein dimensions must be between 256 and 1920")
			}
		}
		fields["width"], fields["height"] = parts[0], parts[1]
		delete(fields, "size")
	}
	for _, key := range []string{"num_steps", "steps"} {
		if value, ok := fields[key]; ok && value != "4" {
			return request, invalidMediaInput("Cloudflare Klein uses exactly 4 steps")
		}
		delete(fields, key)
	}
	for _, name := range []string{"image", "image[]", "images", "mask"} {
		if _, exists := fields[name]; exists {
			return request, invalidMediaInput("Cloudflare Klein reference images must be uploaded as multipart files; masks are not supported")
		}
	}
	used := map[int]bool{}
	for _, file := range files {
		if file.Name == "image" || file.Name == "image[]" {
			continue
		}
		if !strings.HasPrefix(file.Name, "input_image_") {
			return request, invalidMediaInput("Cloudflare Klein supports image or input_image_N uploads, without masks")
		}
		index, err := strconv.Atoi(strings.TrimPrefix(file.Name, "input_image_"))
		if err != nil || index < 0 || index > 3 || used[index] {
			return request, invalidMediaInput("Cloudflare Klein supports up to four distinct reference images")
		}
		used[index] = true
	}
	for i := range files {
		if files[i].Name != "image" && files[i].Name != "image[]" {
			continue
		}
		index := 0
		for used[index] {
			index++
		}
		if index > 3 {
			return request, invalidMediaInput("Cloudflare Klein supports up to four reference images")
		}
		used[index] = true
		files[i].Name = fmt.Sprintf("input_image_%d", index)
	}
	if config.MediaOperationCapability(operation) == config.MediaCapabilityEdit && len(used) == 0 {
		return request, invalidMediaInput("Cloudflare Klein edit requires a reference image")
	}
	request.Payload, request.ContentType, err = writeMediaForm(fields, files)
	request.RequestFormat = config.MediaRequestMultipart
	return request, err
}
