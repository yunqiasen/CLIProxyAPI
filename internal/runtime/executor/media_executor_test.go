package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func mediaExecutorFixture(t *testing.T, upstream http.HandlerFunc) (*MediaExecutor, *cliproxyauth.Auth, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	provider := config.MediaProvider{
		Name:    "Image Relay",
		Kind:    config.MediaKindImage,
		BaseURL: server.URL + "/v1",
		Headers: map[string]string{"X-Provider": "relay"},
		Models: []config.MediaModel{{
			Name:         "upstream-image",
			Alias:        "public-image",
			Capabilities: []string{config.MediaCapabilityGenerate, config.MediaCapabilityEdit},
		}},
	}
	providerKey := util.MediaProviderKey(provider.Kind, provider.Name)
	cfg := &config.Config{MediaProviders: []config.MediaProvider{provider}}
	auth := &cliproxyauth.Auth{
		ID:       "media-auth",
		Provider: providerKey,
		Label:    provider.Name,
		Attributes: map[string]string{
			"api_key":             "secret-key",
			"base_url":            provider.BaseURL,
			"media_kind":          provider.Kind,
			"media_provider_name": provider.Name,
			"provider_name":       provider.Name,
			"provider_key":        providerKey,
			"header:X-Provider":   "relay",
		},
	}
	return NewMediaExecutor(providerKey, cfg), auth, server
}

func mediaImageOptions(path, contentType string) cliproxyexecutor.Options {
	return cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-image"),
		Headers:      http.Header{"Content-Type": []string{contentType}},
		Metadata:     map[string]any{cliproxyexecutor.RequestPathMetadataKey: path},
	}
}

func TestMediaExecutorForwardsJSONImageGeneration(t *testing.T) {
	var received map[string]any
	exec, auth, _ := mediaExecutorFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/images/generations" {
			t.Fatalf("upstream request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret-key" || r.Header.Get("X-Provider") != "relay" {
			t.Fatalf("upstream headers = %#v", r.Header)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"created":1,"data":[{"url":"https://img.example/out.png"}]}`)
	})

	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "public-image",
		Payload: []byte(`{"model":"public-image","prompt":"draw"}`),
	}, mediaImageOptions("/v1/images/generations", "application/json"))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if received["model"] != "upstream-image" || received["prompt"] != "draw" {
		t.Fatalf("upstream payload = %#v", received)
	}
	if !strings.Contains(string(resp.Payload), "out.png") || resp.Headers.Get("Content-Type") != "application/json" {
		t.Fatalf("response = %s headers=%v", resp.Payload, resp.Headers)
	}
}

func TestMediaExecutorForwardsMultipartImageEditAndRewritesModel(t *testing.T) {
	exec, auth, _ := mediaExecutorFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/edits" {
			t.Fatalf("upstream path = %s", r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		if got := r.FormValue("model"); got != "upstream-image" {
			t.Fatalf("model = %q", got)
		}
		file, header, err := r.FormFile("image")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		data, _ := io.ReadAll(file)
		if header.Filename != "input.png" || string(data) != "PNGDATA" || header.Header.Get("Content-Type") != "image/png" {
			t.Fatalf("file = %q %q %q", header.Filename, data, header.Header.Get("Content-Type"))
		}
		_, _ = io.WriteString(w, `{"created":1,"data":[{"b64_json":"AA=="}]}`)
	})

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("model", "public-image")
	_ = writer.WriteField("prompt", "edit")
	partHeader := make(textprotoMIMEHeader)
	partHeader.Set("Content-Disposition", `form-data; name="image"; filename="input.png"`)
	partHeader.Set("Content-Type", "image/png")
	part, err := writer.CreatePart(partHeader.Header())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("PNGDATA"))
	_ = writer.Close()

	_, err = exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "public-image",
		Payload: body.Bytes(),
	}, mediaImageOptions("/v1/images/edits", writer.FormDataContentType()))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
}

type textprotoMIMEHeader map[string][]string

func (h textprotoMIMEHeader) Set(key, value string)       { h[key] = []string{value} }
func (h textprotoMIMEHeader) Header() map[string][]string { return h }

func TestMediaExecutorReturnsRetryAwareUpstreamStatus(t *testing.T) {
	exec, auth, _ := mediaExecutorFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"rate limited"}}`)
	})
	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "public-image",
		Payload: []byte(`{"prompt":"draw"}`),
	}, mediaImageOptions("/v1/images/generations", "application/json"))
	if err == nil {
		t.Fatal("Execute() error = nil")
	}
	status, ok := err.(interface{ StatusCode() int })
	if !ok || status.StatusCode() != http.StatusTooManyRequests || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("error = %#v", err)
	}
}

func TestMediaExecutorRejectsStandardImageOperationMissingModelCapability(t *testing.T) {
	tests := []struct {
		name         string
		path         string
		contentType  string
		payload      []byte
		capabilities []string
	}{
		{
			name: "edit-only model on generation endpoint", path: "/v1/images/generations",
			contentType: "application/json", payload: []byte(`{"model":"public-image","prompt":"draw"}`),
			capabilities: []string{config.MediaCapabilityEdit},
		},
		{
			name: "generation-only model on edit endpoint", path: "/v1/images/edits",
			contentType: "application/json", payload: []byte(`{"model":"public-image","prompt":"edit","image":"data:image/png;base64,AA=="}`),
			capabilities: []string{config.MediaCapabilityGenerate},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var upstreamCalls atomic.Int32
			exec, auth, _ := mediaExecutorFixture(t, func(w http.ResponseWriter, _ *http.Request) {
				upstreamCalls.Add(1)
				_, _ = io.WriteString(w, `{"ok":true}`)
			})
			exec.cfg.MediaProviders[0].Models[0].Capabilities = tt.capabilities

			_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model: "public-image", Payload: tt.payload,
			}, mediaImageOptions(tt.path, tt.contentType))
			if err == nil {
				t.Fatal("Execute() error = nil")
			}
			status, ok := err.(interface{ StatusCode() int })
			if !ok || status.StatusCode() != http.StatusBadRequest || !strings.Contains(err.Error(), "not supported") {
				t.Fatalf("error = %#v", err)
			}
			if upstreamCalls.Load() != 0 {
				t.Fatalf("upstream calls = %d, want 0", upstreamCalls.Load())
			}
		})
	}
}

func TestResolveMediaOperationMatchesConfiguredCapabilityAlias(t *testing.T) {
	provider := &config.MediaProvider{Operations: []config.MediaOperation{{
		Name: "background-cutout", Capability: config.MediaCapabilityRemoveBackground,
		Method: http.MethodPost, Path: "/cutout", RequestFormat: config.MediaRequestJSON,
		ModelMode: config.MediaModelNone, ResponseFormat: config.MediaResponsePassthrough,
	}}}
	operation, _, err := resolveMediaOperation(provider, cliproxyexecutor.Request{}, customMediaOptions(
		config.MediaCapabilityRemoveBackground, "", "application/json",
	))
	if err != nil {
		t.Fatalf("resolveMediaOperation() error = %v", err)
	}
	if operation.Name != "background-cutout" {
		t.Fatalf("operation = %#v", operation)
	}
}

func TestResolveStandardMediaOperationPrefersExactNameBeforeCapabilityAlias(t *testing.T) {
	provider := &config.MediaProvider{Operations: []config.MediaOperation{
		{Name: "custom-generate", Capability: config.MediaCapabilityGenerate},
		{Name: config.MediaCapabilityGenerate, Capability: config.MediaCapabilityEdit},
	}}
	operation, _, err := resolveMediaOperation(provider, cliproxyexecutor.Request{Model: "model"}, mediaImageOptions("/v1/images/generations", "application/json"))
	if err != nil {
		t.Fatalf("resolveMediaOperation() error = %v", err)
	}
	if operation.Name != config.MediaCapabilityGenerate {
		t.Fatalf("operation = %#v, want exact generate operation", operation)
	}
}

func TestResolveMediaOperationFallsBackToRequestModelForCustomOperation(t *testing.T) {
	provider := &config.MediaProvider{Operations: []config.MediaOperation{{
		Name: "upscale", Capability: config.MediaCapabilityUpscale,
		Method: http.MethodPost, Path: "/upscale", RequestFormat: config.MediaRequestJSON,
		ModelMode: config.MediaModelRequired, ResponseFormat: config.MediaResponsePassthrough,
	}}}
	operation, requestedModel, err := resolveMediaOperation(provider, cliproxyexecutor.Request{Model: "public-upscale"}, customMediaOptions(
		"upscale", "", "application/json",
	))
	if err != nil {
		t.Fatalf("resolveMediaOperation() error = %v", err)
	}
	if operation.Name != "upscale" || requestedModel != "public-upscale" {
		t.Fatalf("operation/model = %#v/%q, want upscale/public-upscale", operation, requestedModel)
	}
}

func customMediaExecutorFixture(t *testing.T, upstream http.HandlerFunc, operation config.MediaOperation) (*MediaExecutor, *cliproxyauth.Auth) {
	t.Helper()
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	provider := config.MediaProvider{
		Name: "Custom Images", Kind: config.MediaKindImage, BaseURL: server.URL,
		APIKeyEntries: []config.MediaAPIKeyEntry{{APIKey: "custom-key"}},
		Models:        []config.MediaModel{{Name: "upstream-upscale", Alias: "public-upscale", Capabilities: []string{config.MediaCapabilityGenerate, config.MediaCapabilityUpscale}}},
		Operations:    []config.MediaOperation{operation},
	}
	providerKey := util.MediaProviderKey(provider.Kind, provider.Name)
	cfg := &config.Config{MediaProviders: []config.MediaProvider{provider}}
	auth := &cliproxyauth.Auth{ID: "custom-auth", Provider: providerKey, Label: provider.Name, Attributes: map[string]string{
		"auth_kind": "api_key", "api_key": "custom-key", "base_url": server.URL,
		"media_kind": provider.Kind, "media_provider_name": provider.Name, "provider_name": provider.Name, "provider_key": providerKey,
	}}
	return NewMediaExecutor(providerKey, cfg), auth
}

func customMediaOptions(operation, model, contentType string) cliproxyexecutor.Options {
	return cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("media"),
		Headers:      http.Header{"Content-Type": []string{contentType}},
		Metadata: map[string]any{
			cliproxyexecutor.MediaKindMetadataKey:      config.MediaKindImage,
			cliproxyexecutor.MediaOperationMetadataKey: operation,
			cliproxyexecutor.MediaModelMetadataKey:     model,
			cliproxyexecutor.RequestPathMetadataKey:    "/v1/media/image/" + operation,
		},
	}
}

func TestMediaExecutorCustomNoModelOperationNormalizesJSONURL(t *testing.T) {
	operation := config.MediaOperation{
		Name: "remove-background", Capability: config.MediaCapabilityRemoveBackground,
		Method: http.MethodPost, Path: "/remove", RequestFormat: config.MediaRequestJSON,
		ModelMode: config.MediaModelNone, ResponseFormat: config.MediaResponseJSONURL, ResultPath: "output.url",
	}
	exec, auth := customMediaExecutorFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/remove" {
			t.Fatalf("upstream request = %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		if _, exists := payload["model"]; exists {
			t.Fatalf("model should be removed: %s", body)
		}
		responseBody := `{"output": {"url": "https://img.example/clean.png"}}`
		w.Header().Set("Content-Length", strconv.Itoa(len(responseBody)))
		_, _ = io.WriteString(w, responseBody)
	}, operation)

	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Payload: []byte(`{"model":"fake-model","image":"input"}`),
	}, customMediaOptions(operation.Name, "", "application/json"))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if string(resp.Payload) != `{"data":[{"url":"https://img.example/clean.png"}]}` {
		t.Fatalf("response = %s", resp.Payload)
	}
	if got, want := resp.Headers.Get("Content-Length"), strconv.Itoa(len(resp.Payload)); got != want {
		t.Fatalf("content-length = %q, want %q", got, want)
	}
	if got := resp.Headers.Get("Content-Type"); got != "application/json" {
		t.Fatalf("normalized content-type = %q, want application/json", got)
	}
}

func TestMediaExecutorRejectsHTTP200ApplicationError(t *testing.T) {
	operation := config.MediaOperation{
		Name: "clone", Capability: config.MediaCapabilityClone,
		Method: http.MethodPost, Path: "/voices", RequestFormat: config.MediaRequestJSON,
		ModelMode: config.MediaModelNone, ResponseFormat: config.MediaResponsePassthrough,
	}
	exec, auth := customMediaExecutorFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":400,"message":"credit limit exceeded"}`)
	}, operation)

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Payload: []byte(`{"input":"voice"}`),
	}, customMediaOptions(operation.Name, "", "application/json"))
	if err == nil {
		t.Fatal("Execute() accepted an application-level error")
	}
	status, ok := err.(interface{ StatusCode() int })
	if !ok || status.StatusCode() != http.StatusBadGateway {
		t.Fatalf("status error = %T %v, want 502", err, err)
	}
	if !strings.Contains(err.Error(), "code 400") || !strings.Contains(err.Error(), "credit limit exceeded") {
		t.Fatalf("error = %v", err)
	}
}

func TestMediaExecutorRejectsHTTP200NonSuccessBusinessCodes(t *testing.T) {
	operation := config.MediaOperation{
		Name: "clone", Capability: config.MediaCapabilityClone,
		Method: http.MethodPost, Path: "/voices", RequestFormat: config.MediaRequestJSON,
		ModelMode: config.MediaModelNone, ResponseFormat: config.MediaResponsePassthrough,
	}
	for _, body := range []string{
		`{"code":-1,"message":"invalid key"}`,
		`{"code":1001,"message":"insufficient balance"}`,
		`{"code":"fail_to_fetch_task","message":"model is blocked"}`,
	} {
		t.Run(body, func(t *testing.T) {
			exec, auth := customMediaExecutorFixture(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, body)
			}, operation)

			_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Payload: []byte(`{"input":"voice"}`),
			}, customMediaOptions(operation.Name, "", "application/json"))
			if err == nil {
				t.Fatalf("Execute() accepted application error %s", body)
			}
		})
	}
}

func TestMediaExecutorAcceptsHTTP200SuccessCodeAndPayloadWarning(t *testing.T) {
	operation := config.MediaOperation{
		Name: "clone", Capability: config.MediaCapabilityClone,
		Method: http.MethodPost, Path: "/voices", RequestFormat: config.MediaRequestJSON,
		ModelMode: config.MediaModelNone, ResponseFormat: config.MediaResponsePassthrough,
	}
	for _, body := range []string{
		`{"code":201,"data":{"task_id":"task-1"}}`,
		`{"ok":true,"data":{"task_id":"task-1"},"error":"fallback voice used"}`,
	} {
		t.Run(body, func(t *testing.T) {
			exec, auth := customMediaExecutorFixture(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, body)
			}, operation)
			if _, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{Payload: []byte(`{"input":"voice"}`)}, customMediaOptions(operation.Name, "", "application/json")); err != nil {
				t.Fatalf("Execute() rejected successful payload %s: %v", body, err)
			}
		})
	}
}

func TestMediaExecutorAcceptsHTTP200VendorSuccessCode(t *testing.T) {
	operation := config.MediaOperation{
		Name: "clone", Capability: config.MediaCapabilityClone,
		Method: http.MethodPost, Path: "/voices", RequestFormat: config.MediaRequestJSON,
		ModelMode: config.MediaModelNone, ResponseFormat: config.MediaResponsePassthrough,
	}
	exec, auth := customMediaExecutorFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":10000,"data":{"voice_id":"voice-1"}}`)
	}, operation)

	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Payload: []byte(`{"input":"voice"}`),
	}, customMediaOptions(operation.Name, "", "application/json"))
	if err != nil {
		t.Fatalf("Execute() rejected vendor success code: %v", err)
	}
	if !strings.Contains(string(resp.Payload), `"voice_id":"voice-1"`) {
		t.Fatalf("response = %s", resp.Payload)
	}
}

func TestMediaExecutorCustomOperationRewritesAliasAndUsesConfiguredPath(t *testing.T) {
	operation := config.MediaOperation{
		Name: "upscale", Capability: config.MediaCapabilityUpscale,
		Method: http.MethodPut, Path: "/upscale", RequestFormat: config.MediaRequestJSON,
		ModelMode: config.MediaModelRequired, ResponseFormat: config.MediaResponsePassthrough,
	}
	exec, auth := customMediaExecutorFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/upscale" {
			t.Fatalf("upstream request = %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"upstream-upscale"`) {
			t.Fatalf("upstream payload = %s", body)
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}, operation)

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Payload: []byte(`{"model":"public-upscale","image":"input"}`),
	}, customMediaOptions(operation.Name, "public-upscale", "application/json"))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestMediaExecutorAsyncOperationPollsWithSameCredential(t *testing.T) {
	polls := 0
	operation := config.MediaOperation{
		Name: "async-upscale", Capability: config.MediaCapabilityUpscale,
		Method: http.MethodPost, Path: "/submit", RequestFormat: config.MediaRequestJSON,
		ModelMode: config.MediaModelNone, ResponseFormat: config.MediaResponseJSONURL, ResultPath: "submit.url",
		Async: &config.MediaAsyncOperation{
			TaskIDPath: "task_id", PollMethod: http.MethodGet, PollPath: "/tasks/{task_id}", StatusPath: "status",
			SuccessValues: []string{"completed"}, FailureValues: []string{"failed"}, ResultPath: "output.url", PollInterval: "1ms",
		},
	}
	exec, auth := customMediaExecutorFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer custom-key" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/submit":
			_, _ = io.WriteString(w, `{"task_id":"task-1"}`)
		case "/tasks/task-1":
			polls++
			if polls == 1 {
				_, _ = io.WriteString(w, `{"status":"running"}`)
				return
			}
			_, _ = io.WriteString(w, `{"status":"completed","output":{"url":"https://img.example/final.png"}}`)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}, operation)

	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{Payload: []byte(`{"image":"input"}`)}, customMediaOptions(operation.Name, "", "application/json"))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if polls != 2 || !strings.Contains(string(resp.Payload), "final.png") {
		t.Fatalf("polls=%d response=%s", polls, resp.Payload)
	}
}

func TestMediaExecutorRewritesModelInQueryForBinaryOperation(t *testing.T) {
	operation := config.MediaOperation{
		Name: "upscale-query", Capability: config.MediaCapabilityUpscale,
		Method: http.MethodGet, Path: "/upscale", RequestFormat: config.MediaRequestBinary,
		ModelMode: config.MediaModelRequired, ResponseFormat: config.MediaResponsePassthrough,
	}
	exec, auth := customMediaExecutorFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("model"); got != "upstream-upscale" {
			t.Fatalf("query model = %q", got)
		}
		if r.URL.Query().Get("keep") != "yes" {
			t.Fatalf("query was not preserved: %s", r.URL.RawQuery)
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}, operation)

	options := customMediaOptions(operation.Name, "public-upscale", "application/octet-stream")
	options.Query = mapQuery("model", "public-upscale", "keep", "yes")
	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{Payload: []byte("binary")}, options)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if string(resp.Payload) != `{"ok":true}` {
		t.Fatalf("response = %s", resp.Payload)
	}
}

func TestMediaExecutorRemovesModelFromQueryForModelFreeOperation(t *testing.T) {
	operation := config.MediaOperation{
		Name: "remove-query-model", Capability: config.MediaCapabilityRemoveBackground,
		Method: http.MethodGet, Path: "/remove", RequestFormat: config.MediaRequestBinary,
		ModelMode: config.MediaModelNone, ResponseFormat: config.MediaResponsePassthrough,
	}
	exec, auth := customMediaExecutorFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if _, exists := r.URL.Query()["model"]; exists {
			t.Fatalf("model query leaked: %s", r.URL.RawQuery)
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}, operation)
	options := customMediaOptions(operation.Name, "", "application/octet-stream")
	options.Query = mapQuery("model", "public-upscale", "keep", "yes")
	if _, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{Payload: []byte("binary")}, options); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestJoinMediaURLAvoidsDuplicateBasePathPrefix(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		path    string
		want    string
	}{
		{name: "v1 overlap", baseURL: "https://relay.example/v1", path: "/v1/images/generations", want: "https://relay.example/v1/images/generations"},
		{name: "nested overlap", baseURL: "https://relay.example/api/v1", path: "/v1/tasks/1", want: "https://relay.example/api/v1/tasks/1"},
		{name: "no overlap", baseURL: "https://relay.example/api", path: "/v1/images", want: "https://relay.example/api/v1/images"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := joinMediaURL(tt.baseURL, tt.path, nil); got != tt.want {
				t.Fatalf("joinMediaURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func mapQuery(values ...string) url.Values {
	query := url.Values{}
	for index := 0; index+1 < len(values); index += 2 {
		query.Set(values[index], values[index+1])
	}
	return query
}

func TestMediaModelCapabilityErrorAllowsCredentialFallback(t *testing.T) {
	provider := &config.MediaProvider{
		Models: []config.MediaModel{{
			Name:         "upstream-image",
			Alias:        "shared-image",
			Capabilities: []string{config.MediaCapabilityGenerate},
		}},
	}
	errorValue := validateMediaModelCapability(provider, config.MediaOperation{
		Name:       config.MediaCapabilityEdit,
		Capability: config.MediaCapabilityEdit,
		ModelMode:  config.MediaModelRequired,
	}, "shared-image")
	if errorValue == nil {
		t.Fatal("expected capability mismatch error")
	}
	if requestScoped, ok := errorValue.(cliproxyexecutor.RequestScopedError); ok && requestScoped.IsRequestScoped() {
		t.Fatalf("capability mismatch must allow another credential, got request-scoped error: %v", errorValue)
	}
}

func TestMediaExecutorCapabilityMismatchFallsBackToAnotherProvider(t *testing.T) {
	var firstCalls atomic.Int32
	firstServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		firstCalls.Add(1)
		_, _ = io.WriteString(w, `{"unexpected":true}`)
	}))
	t.Cleanup(firstServer.Close)
	var secondCalls atomic.Int32
	secondServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalls.Add(1)
		if r.URL.Path != "/v1/images/edits" {
			t.Fatalf("fallback upstream path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(secondServer.Close)

	providers := []config.MediaProvider{
		{
			Name:    "Generate Relay",
			Kind:    config.MediaKindImage,
			BaseURL: firstServer.URL + "/v1",
			Models: []config.MediaModel{{
				Name: "generate-model", Alias: "shared-image", Capabilities: []string{config.MediaCapabilityGenerate},
			}},
		},
		{
			Name:    "Edit Relay",
			Kind:    config.MediaKindImage,
			BaseURL: secondServer.URL + "/v1",
			Models: []config.MediaModel{{
				Name: "edit-model", Alias: "shared-image", Capabilities: []string{config.MediaCapabilityEdit},
			}},
		},
	}
	cfg := &config.Config{MediaProviders: providers}
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.SetRetryConfig(0, 0, 2)

	registryRef := registry.GetGlobalRegistry()
	for index := range providers {
		provider := &providers[index]
		providerKey := util.MediaProviderKey(provider.Kind, provider.Name)
		manager.RegisterExecutor(NewMediaExecutor(providerKey, cfg))
		auth := &cliproxyauth.Auth{
			ID:       "media-fallback-" + strings.ToLower(string(rune('a'+index))),
			Provider: providerKey,
			Status:   cliproxyauth.StatusActive,
			Attributes: map[string]string{
				"api_key":             "key-" + providerKey,
				"base_url":            provider.BaseURL,
				"media_kind":          provider.Kind,
				"media_provider_name": provider.Name,
				"provider_key":        providerKey,
				"priority":            strconv.Itoa(20 - index*10),
			},
		}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register auth %s: %v", providerKey, errRegister)
		}
		registryRef.RegisterClient(auth.ID, providerKey, []*registry.ModelInfo{{
			ID: "shared-image", Type: registry.OpenAIImageModelType,
		}})
		t.Cleanup(func() { registryRef.UnregisterClient(auth.ID) })
	}

	resp, err := manager.Execute(context.Background(), []string{
		util.MediaProviderKey(config.MediaKindImage, providers[0].Name),
		util.MediaProviderKey(config.MediaKindImage, providers[1].Name),
	}, cliproxyexecutor.Request{
		Model:   "shared-image",
		Payload: []byte(`{"model":"shared-image","prompt":"edit","image":"data:image/png;base64,AA=="}`),
	}, mediaImageOptions("/v1/images/edits", "application/json"))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if string(resp.Payload) != `{"ok":true}` {
		t.Fatalf("response = %s", resp.Payload)
	}
	if firstCalls.Load() != 0 || secondCalls.Load() != 1 {
		t.Fatalf("upstream calls = first:%d second:%d, want first:0 second:1", firstCalls.Load(), secondCalls.Load())
	}
}

func TestMediaExecutorExpandsModelPathAndOmitsBodyModel(t *testing.T) {
	operation := config.MediaOperation{
		Name: config.MediaCapabilityUpscale, Capability: config.MediaCapabilityUpscale,
		Method: http.MethodPost, Path: "/{model}", RequestFormat: config.MediaRequestJSON,
		ModelMode: config.MediaModelRequired, ResponseFormat: config.MediaResponsePassthrough,
	}
	exec, auth := customMediaExecutorFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/upstream-upscale" {
			t.Fatalf("upstream path = %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"model"`) {
			t.Fatalf("model leaked into body: %s", body)
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}, operation)

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Payload: []byte(`{"model":"public-upscale","prompt":"draw"}`),
	}, customMediaOptions(operation.Name, "public-upscale", "application/json"))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestMediaExecutorUsesConfiguredCredentialHeaderAndBarePrefix(t *testing.T) {
	operation := config.MediaOperation{
		Name: config.MediaCapabilityRemoveBackground, Capability: config.MediaCapabilityRemoveBackground,
		Method: http.MethodPost, Path: "/remove", RequestFormat: config.MediaRequestJSON,
		ModelMode: config.MediaModelNone, ResponseFormat: config.MediaResponsePassthrough,
	}
	exec, auth := customMediaExecutorFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-API-Key"); got != "custom-key" {
			t.Fatalf("X-API-Key = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("Authorization = %q, want empty", got)
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}, operation)
	auth.Attributes["api_key_header"] = "X-API-Key"
	auth.Attributes["api_key_prefix"] = "-"

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Payload: []byte(`{"input":"data"}`),
	}, customMediaOptions(operation.Name, "", "application/json"))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestMediaApplicationErrorAcceptsUnknownCodeWithResultPayload(t *testing.T) {
	if err := mediaApplicationError([]byte(`{"code":1,"data":{"url":"https://img.example/out.png"}}`)); err != nil {
		t.Fatalf("mediaApplicationError() = %v, want nil for payload-bearing response", err)
	}
}

func TestMediaApplicationErrorAcceptsVendorSuccessCode1000(t *testing.T) {
	if err := mediaApplicationError([]byte(`{"code":1000,"data":{"url":"https://img.example/out.png"}}`)); err != nil {
		t.Fatalf("mediaApplicationError() = %v, want nil", err)
	}
}
