package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
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
