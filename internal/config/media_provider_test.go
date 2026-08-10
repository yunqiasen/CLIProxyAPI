package config

import "testing"

func TestParseConfigBytesMediaProviders(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
media-providers:
  - name: " Image Relay "
    kind: IMAGE
    base-url: " https://image.example/v1/ "
    priority: 8
    api-key-entries:
      - api-key: " key-a "
        priority: 0
      - api-key: " key-b "
        priority: 20
        proxy-url: " http://key-proxy "
    headers:
      X-Test: " value "
    models:
      - name: " upstream-image "
        alias: " public-image "
        display-name: " Image Model "
        capabilities: [GENERATE, edit, generate, unknown]
    operations:
      - name: " remove-background "
        capability: " remove-background "
        method: post
        path: " images/remove-background "
        request-format: MULTIPART
        model-mode: NONE
        response-format: JSON-URL
        result-path: " data.0.url "
        test-request:
          multipart-fields:
            prompt: test
        async:
          task-id-path: " task_id "
          poll-method: get
          poll-path: " tasks/{task_id} "
          status-path: " task_status "
          success-values: [" SUCCEED "]
          failure-values: [" FAILED "]
          result-path: " output_images.0 "
          poll-interval: " 2s "
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if len(cfg.MediaProviders) != 1 {
		t.Fatalf("len(MediaProviders) = %d, want 1", len(cfg.MediaProviders))
	}
	provider := cfg.MediaProviders[0]
	if provider.Name != "Image Relay" || provider.Kind != MediaKindImage || provider.BaseURL != "https://image.example/v1" {
		t.Fatalf("provider normalization = %#v", provider)
	}
	if len(provider.APIKeyEntries) != 2 || provider.APIKeyEntries[0].Priority == nil || *provider.APIKeyEntries[0].Priority != 0 {
		t.Fatalf("key entries = %#v", provider.APIKeyEntries)
	}
	if provider.APIKeyEntries[1].ProxyURL != "http://key-proxy" || *provider.APIKeyEntries[1].Priority != 20 {
		t.Fatalf("second key = %#v", provider.APIKeyEntries[1])
	}
	if len(provider.Models) != 1 || provider.Models[0].Name != "upstream-image" || provider.Models[0].Alias != "public-image" {
		t.Fatalf("models = %#v", provider.Models)
	}
	wantCaps := []string{MediaCapabilityGenerate, MediaCapabilityEdit}
	if len(provider.Models[0].Capabilities) != len(wantCaps) || provider.Models[0].Capabilities[0] != wantCaps[0] || provider.Models[0].Capabilities[1] != wantCaps[1] {
		t.Fatalf("capabilities = %#v, want %#v", provider.Models[0].Capabilities, wantCaps)
	}
	if len(provider.Operations) != 1 {
		t.Fatalf("operations = %#v", provider.Operations)
	}
	op := provider.Operations[0]
	if op.Method != "POST" || op.Path != "/images/remove-background" || op.RequestFormat != MediaRequestMultipart || op.ModelMode != MediaModelNone || op.ResponseFormat != MediaResponseJSONURL {
		t.Fatalf("operation normalization = %#v", op)
	}
	if op.TestRequest == nil || op.TestRequest.MultipartFields["prompt"] != "test" {
		t.Fatalf("test request normalization = %#v", op.TestRequest)
	}
	if op.Async == nil || op.Async.PollMethod != "GET" || op.Async.PollPath != "/tasks/{task_id}" || op.Async.PollInterval != "2s" {
		t.Fatalf("async normalization = %#v", op.Async)
	}
}

func TestSanitizeMediaProvidersDropsInvalidAndDuplicateEntries(t *testing.T) {
	zero := 0
	cfg := &Config{MediaProviders: []MediaProvider{
		{Name: "", Kind: MediaKindImage, BaseURL: "https://missing-name"},
		{Name: "bad-kind", Kind: "document", BaseURL: "https://bad-kind"},
		{Name: "bad-url", Kind: MediaKindImage, BaseURL: ""},
		{
			Name: "valid", Kind: MediaKindImage, BaseURL: "https://valid/v1/",
			APIKeyEntries: []MediaAPIKeyEntry{{APIKey: " a ", Priority: &zero}, {APIKey: "a"}, {APIKey: " "}},
			Models:        []MediaModel{{Name: " model ", Capabilities: []string{MediaCapabilityGenerate}}, {Name: " "}},
			Operations: []MediaOperation{
				{Name: "op", Method: "POST", Path: "/op", RequestFormat: MediaRequestJSON, ModelMode: MediaModelNone, ResponseFormat: MediaResponsePassthrough},
				{Name: "OP", Method: "POST", Path: "/duplicate", RequestFormat: MediaRequestJSON, ModelMode: MediaModelNone, ResponseFormat: MediaResponsePassthrough},
				{Name: "bad-mode", Method: "POST", Path: "/bad", RequestFormat: "xml", ModelMode: MediaModelNone, ResponseFormat: MediaResponsePassthrough},
			},
		},
	}}
	cfg.SanitizeMediaProviders()
	if len(cfg.MediaProviders) != 1 {
		t.Fatalf("providers = %#v", cfg.MediaProviders)
	}
	provider := cfg.MediaProviders[0]
	if provider.BaseURL != "https://valid/v1" || len(provider.APIKeyEntries) != 1 || len(provider.Models) != 1 || len(provider.Operations) != 1 {
		t.Fatalf("sanitized provider = %#v", provider)
	}
}

func TestCloneForRuntimeDeepCopiesMediaProvider(t *testing.T) {
	priority := 3
	cfg := &Config{MediaProviders: []MediaProvider{{
		Name: "image", Kind: MediaKindImage, BaseURL: "https://image.example",
		APIKeyEntries: []MediaAPIKeyEntry{{APIKey: "key", Priority: &priority}},
		Models:        []MediaModel{{Name: "model", Capabilities: []string{MediaCapabilityGenerate}}},
		Operations:    []MediaOperation{{Name: "op", Async: &MediaAsyncOperation{SuccessValues: []string{"done"}}}},
	}}}
	clone := cfg.CloneForRuntime()
	*clone.MediaProviders[0].APIKeyEntries[0].Priority = 9
	clone.MediaProviders[0].Models[0].Capabilities[0] = MediaCapabilityEdit
	clone.MediaProviders[0].Operations[0].Async.SuccessValues[0] = "changed"
	if *cfg.MediaProviders[0].APIKeyEntries[0].Priority != 3 || cfg.MediaProviders[0].Models[0].Capabilities[0] != MediaCapabilityGenerate || cfg.MediaProviders[0].Operations[0].Async.SuccessValues[0] != "done" {
		t.Fatalf("CloneForRuntime shared media provider storage: %#v", cfg.MediaProviders[0])
	}
}
