package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexExecutorExecuteResponsesLiteHeaderDoesNotInjectImageGenerationTool(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"total_tokens\":0}}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":   "test",
			"base_url":  server.URL,
			"plan_type": "pro",
		},
	}
	headers := make(http.Header)
	headers.Set("X-OpenAI-Internal-Codex-Responses-Lite", "true")

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: []byte(`{"model":"gpt-5.6-sol","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Headers:      headers,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if tools := gjson.GetBytes(gotBody, "tools"); tools.Exists() {
		t.Fatalf("unexpected tools in responses-lite upstream payload: %s", tools.Raw)
	}
	parallelToolCalls := gjson.GetBytes(gotBody, "parallel_tool_calls")
	if !parallelToolCalls.Exists() || parallelToolCalls.Bool() {
		t.Fatalf("responses-lite parallel_tool_calls should be false: %s", gotBody)
	}
}

func TestCodexExecutorExecuteStreamResponsesLiteHeaderForcesParallelToolCallsFalse(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"total_tokens\":0}}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":   "test",
			"base_url":  server.URL,
			"plan_type": "pro",
		},
	}
	headers := make(http.Header)
	headers.Set(codexResponsesLiteHeader, "true")

	result, errExecute := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6-luna",
		Payload: []byte(`{"model":"gpt-5.6-luna","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Headers:      headers,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
	}

	parallelToolCalls := gjson.GetBytes(gotBody, "parallel_tool_calls")
	if !parallelToolCalls.Exists() || parallelToolCalls.Bool() {
		t.Fatalf("responses-lite parallel_tool_calls should be false: %s", gotBody)
	}
}

func TestEnsureImageGenerationTool_ResponsesLiteMetadataDoesNotInjectTool(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"},"input":[{"role":"user","content":"hello"}]}`)
	result := ensureImageGenerationTool(body, "gpt-5.6-sol", nil, nil)

	if string(result) != string(body) {
		t.Fatalf("expected responses-lite body to be unchanged, got %s", string(result))
	}
	if gjson.GetBytes(result, "tools").Exists() {
		t.Fatalf("expected no injected tools for responses-lite request, got %s", gjson.GetBytes(result, "tools").Raw)
	}
}

func TestEnsureImageGenerationTool_ResponsesLiteBooleanMetadataDoesNotInjectTool(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":true},"input":"hello"}`)
	result := ensureImageGenerationTool(body, "gpt-5.6-sol", nil, nil)

	if string(result) != string(body) {
		t.Fatalf("expected responses-lite body to be unchanged, got %s", string(result))
	}
}

func TestEnsureImageGenerationTool_ResponsesLiteHeaderDoesNotInjectTool(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","input":"hello"}`)
	headers := make(http.Header)
	headers.Set("X-OpenAI-Internal-Codex-Responses-Lite", "true")
	result := ensureImageGenerationTool(body, "gpt-5.6-sol", nil, headers)

	if string(result) != string(body) {
		t.Fatalf("expected responses-lite body to be unchanged, got %s", string(result))
	}
}

func TestEnsureImageGenerationTool_ResponsesLiteFalseMetadataStillInjectsTool(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"false"},"input":"hello"}`)
	result := ensureImageGenerationTool(body, "gpt-5.6-sol", nil, nil)

	if got := gjson.GetBytes(result, "tools.0.type").String(); got != "image_generation" {
		t.Fatalf("tools.0.type = %q, want image_generation; body=%s", got, result)
	}
}

func TestEnsureImageGenerationTool_NoTools(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"draw a cat"}`)
	result := ensureImageGenerationTool(body, "gpt-5.4", nil, nil)

	tools := gjson.GetBytes(result, "tools")
	if !tools.IsArray() {
		t.Fatalf("expected tools array, got %v", tools.Type)
	}
	arr := tools.Array()
	if len(arr) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(arr))
	}
	if arr[0].Get("type").String() != "image_generation" {
		t.Fatalf("expected type=image_generation, got %s", arr[0].Get("type").String())
	}
	if arr[0].Get("output_format").String() != "png" {
		t.Fatalf("expected output_format=png, got %s", arr[0].Get("output_format").String())
	}
}

func TestEnsureImageGenerationTool_ExistingToolsWithoutImageGen(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[{"type":"function","name":"get_weather","parameters":{}}]}`)
	result := ensureImageGenerationTool(body, "gpt-5.4", nil, nil)

	tools := gjson.GetBytes(result, "tools")
	arr := tools.Array()
	if len(arr) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(arr))
	}
	if arr[0].Get("type").String() != "function" {
		t.Fatalf("expected first tool type=function, got %s", arr[0].Get("type").String())
	}
	if arr[1].Get("type").String() != "image_generation" {
		t.Fatalf("expected second tool type=image_generation, got %s", arr[1].Get("type").String())
	}
}

func TestEnsureImageGenerationTool_AlreadyPresent(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[{"type":"image_generation","output_format":"webp"},{"type":"function","name":"f1"}]}`)
	result := ensureImageGenerationTool(body, "gpt-5.4", nil, nil)

	tools := gjson.GetBytes(result, "tools")
	arr := tools.Array()
	if len(arr) != 2 {
		t.Fatalf("expected 2 tools (no duplicate), got %d", len(arr))
	}
	if arr[0].Get("output_format").String() != "webp" {
		t.Fatalf("expected original output_format=webp preserved, got %s", arr[0].Get("output_format").String())
	}
}

func TestEnsureImageGenerationTool_ImageGenNamespaceDoesNotInjectTool(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[{"type":"namespace","name":"image_gen","tools":[{"type":"function","name":"imagegen","parameters":{}}]}]}`)
	result := ensureImageGenerationTool(body, "gpt-5.4", nil, nil)

	if string(result) != string(body) {
		t.Fatalf("expected body to be unchanged, got %s", string(result))
	}
}

func TestEnsureImageGenerationTool_FlattenedImageGenFunctionDoesNotInjectTool(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[{"type":"function","name":"image_gen.imagegen","parameters":{}}]}`)
	result := ensureImageGenerationTool(body, "gpt-5.4", nil, nil)

	if string(result) != string(body) {
		t.Fatalf("expected body to be unchanged, got %s", string(result))
	}
}

func TestEnsureImageGenerationTool_SimilarNamespaceStillInjectsTool(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[{"type":"namespace","name":"image_tools","tools":[{"type":"function","name":"imagegen","parameters":{}}]}]}`)
	result := ensureImageGenerationTool(body, "gpt-5.4", nil, nil)

	tools := gjson.GetBytes(result, "tools").Array()
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	if tools[1].Get("type").String() != "image_generation" {
		t.Fatalf("expected second tool type=image_generation, got %s", tools[1].Get("type").String())
	}
}

func TestEnsureImageGenerationTool_EmptyToolsArray(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[]}`)
	result := ensureImageGenerationTool(body, "gpt-5.4", nil, nil)

	tools := gjson.GetBytes(result, "tools")
	arr := tools.Array()
	if len(arr) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(arr))
	}
	if arr[0].Get("type").String() != "image_generation" {
		t.Fatalf("expected type=image_generation, got %s", arr[0].Get("type").String())
	}
}

func TestEnsureImageGenerationTool_WebSearchAndImageGen(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[{"type":"web_search"}]}`)
	result := ensureImageGenerationTool(body, "gpt-5.4", nil, nil)

	tools := gjson.GetBytes(result, "tools")
	arr := tools.Array()
	if len(arr) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(arr))
	}
	if arr[0].Get("type").String() != "web_search" {
		t.Fatalf("expected first tool type=web_search, got %s", arr[0].Get("type").String())
	}
	if arr[1].Get("type").String() != "image_generation" {
		t.Fatalf("expected second tool type=image_generation, got %s", arr[1].Get("type").String())
	}
}

func TestEnsureImageGenerationTool_GPT53CodexSparkDoesNotInjectTool(t *testing.T) {
	body := []byte(`{"model":"gpt-5.3-codex-spark","input":"draw a cat"}`)
	result := ensureImageGenerationTool(body, "gpt-5.3-codex-spark", nil, nil)

	if string(result) != string(body) {
		t.Fatalf("expected body to be unchanged, got %s", string(result))
	}
	if gjson.GetBytes(result, "tools").Exists() {
		t.Fatalf("expected no tools for gpt-5.3-codex-spark, got %s", gjson.GetBytes(result, "tools").Raw)
	}
}

func TestEnsureImageGenerationTool_FreeCodexAuthDoesNotInjectTool(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"draw a cat"}`)
	freeAuth := &cliproxyauth.Auth{
		Provider:   "codex",
		Attributes: map[string]string{"plan_type": "free"},
	}
	result := ensureImageGenerationTool(body, "gpt-5.4", freeAuth, nil)

	if string(result) != string(body) {
		t.Fatalf("expected body to be unchanged, got %s", string(result))
	}
	if gjson.GetBytes(result, "tools").Exists() {
		t.Fatalf("expected no tools for free codex auth, got %s", gjson.GetBytes(result, "tools").Raw)
	}
}

func TestEnsureImageGenerationTool_DisabledProviderRemovesAllImageToolForms(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":                  "test",
		"disable_image_generation": "true",
	}}
	body := []byte(`{"model":"gpt-5.6-sol","tools":[{"type":"image_generation"},{"type":"function","name":"image_gen.imagegen"},{"type":"namespace","name":"image_gen","tools":[{"type":"function","name":"imagegen"}]},{"type":"function","name":"keep_me"}],"tool_choice":{"type":"image_generation"}}`)

	result := ensureImageGenerationTool(body, "gpt-5.6-sol", auth, nil)
	tools := gjson.GetBytes(result, "tools")
	if !tools.Exists() || len(tools.Array()) != 1 {
		t.Fatalf("tools = %s, want only unrelated tool", tools.Raw)
	}
	if tools.Array()[0].Get("name").String() != "keep_me" {
		t.Fatalf("remaining tool = %s, want keep_me", tools.Array()[0].Raw)
	}
	if gjson.GetBytes(result, "tool_choice").Exists() {
		t.Fatalf("tool_choice was not removed: %s", result)
	}
}

func TestEnsureImageGenerationTool_DisabledProviderDoesNotInjectImageTool(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":                  "test",
		"disable_image_generation": "true",
	}}
	result := ensureImageGenerationTool([]byte(`{"model":"gpt-5.6-sol","input":"hello"}`), "gpt-5.6-sol", auth, nil)
	if gjson.GetBytes(result, "tools").Exists() {
		t.Fatalf("image tool was injected for disabled provider: %s", result)
	}
}

func TestEnsureImageGenerationTool_DisabledProviderRemovesNestedFunctionShape(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		cliproxyauth.AttributeCodexDisableImageGeneration: "true",
	}}
	body := []byte(`{"model":"gpt-5.6-sol","tools":[{"type":"function","function":{"name":"image_gen.imagegen","description":"generate"}},{"type":"function","function":{"name":"keep_me"}}],"tool_choice":{"type":"function","function":{"name":"image_gen.imagegen"}},"parallel_tool_calls":true}`)

	result := ensureImageGenerationTool(body, "gpt-5.6-sol", auth, nil)
	tools := gjson.GetBytes(result, "tools").Array()
	if len(tools) != 1 || tools[0].Get("function.name").String() != "keep_me" {
		t.Fatalf("tools = %s, want only nested keep_me function", gjson.GetBytes(result, "tools").Raw)
	}
	if gjson.GetBytes(result, "tool_choice").Exists() {
		t.Fatalf("nested image tool_choice was not removed: %s", result)
	}
	if !gjson.GetBytes(result, "parallel_tool_calls").Bool() {
		t.Fatalf("parallel_tool_calls should remain while an unrelated tool exists: %s", result)
	}
}

func TestEnsureImageGenerationTool_DisabledProviderRemovesRequiredChoiceWhenToolsBecomeEmpty(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		cliproxyauth.AttributeCodexDisableImageGeneration: "true",
	}}
	body := []byte(`{"model":"gpt-5.6-sol","tools":[{"type":"image_generation"}],"tool_choice":"required","parallel_tool_calls":true}`)

	result := ensureImageGenerationTool(body, "gpt-5.6-sol", auth, nil)
	if tools := gjson.GetBytes(result, "tools"); !tools.Exists() || len(tools.Array()) != 0 {
		t.Fatalf("tools = %s, want empty array", tools.Raw)
	}
	if gjson.GetBytes(result, "tool_choice").Exists() {
		t.Fatalf("tool_choice should be removed after all tools are stripped: %s", result)
	}
	if gjson.GetBytes(result, "parallel_tool_calls").Exists() {
		t.Fatalf("parallel_tool_calls should be removed after all tools are stripped: %s", result)
	}
}

func TestEnsureImageGenerationTool_DisabledProviderNormalizesEmptyTools(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		cliproxyauth.AttributeCodexDisableImageGeneration: "true",
	}}
	body := []byte(`{"model":"gpt-5.6-sol","tools":[{"type":"image_generation"},{"type":"namespace","name":"image_gen","tools":[{"type":"function","name":"imagegen"}]}],"parallel_tool_calls":true}`)

	result := ensureImageGenerationTool(body, "gpt-5.6-sol", auth, nil)
	if tools := gjson.GetBytes(result, "tools"); !tools.Exists() || len(tools.Array()) != 0 {
		t.Fatalf("tools = %s, want empty array", tools.Raw)
	}
	if gjson.GetBytes(result, "parallel_tool_calls").Exists() {
		t.Fatalf("parallel_tool_calls should be removed after all tools are stripped: %s", result)
	}
}

func TestEnsureImageGenerationTool_DisabledProviderPreservesOtherNamespaceFunctions(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		cliproxyauth.AttributeCodexDisableImageGeneration: "true",
	}}
	body := []byte(`{"model":"gpt-5.6-sol","tools":[{"type":"namespace","name":"image_gen","tools":[{"type":"function","name":"imagegen"},{"type":"function","name":"inspect"}]}]}`)

	result := ensureImageGenerationTool(body, "gpt-5.6-sol", auth, nil)
	tools := gjson.GetBytes(result, "tools").Array()
	if len(tools) != 1 {
		t.Fatalf("tools = %s, want preserved namespace", gjson.GetBytes(result, "tools").Raw)
	}
	nested := tools[0].Get("tools").Array()
	if len(nested) != 1 || nested[0].Get("name").String() != "inspect" {
		t.Fatalf("namespace tools = %s, want only inspect", tools[0].Get("tools").Raw)
	}
}

func TestCodexExecutorExecuteDisabledProviderStripsImageGenBeforeHTTPDispatch(t *testing.T) {
	assertCodexHTTPImageGenSuppressed(t, false)
}

func TestCodexExecutorExecuteStreamDisabledProviderStripsImageGenBeforeHTTPDispatch(t *testing.T) {
	assertCodexHTTPImageGenSuppressed(t, true)
}

func assertCodexHTTPImageGenSuppressed(t *testing.T, stream bool) {
	t.Helper()
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read request body: %v", errRead)
			return
		}
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"total_tokens\":0}}}\n\n"))
	}))
	defer server.Close()

	exec := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Provider: "codex", Attributes: map[string]string{
		cliproxyauth.AttributeAPIKey: "test",
		"base_url":                   server.URL,
		cliproxyauth.AttributeCodexDisableImageGeneration: "true",
	}}
	payload := []byte(`{"model":"gpt-5.6-sol","input":"hello","tools":[{"type":"function","function":{"name":"image_gen.imagegen"}},{"type":"image_generation"}],"tool_choice":{"type":"function","function":{"name":"image_gen.imagegen"}},"parallel_tool_calls":true}`)
	req := cliproxyexecutor.Request{Model: "gpt-5.6-sol", Payload: payload}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("codex")}

	if stream {
		result, errExecute := exec.ExecuteStream(context.Background(), auth, req, opts)
		if errExecute != nil {
			t.Fatalf("ExecuteStream() error = %v", errExecute)
		}
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				t.Fatalf("stream chunk error = %v", chunk.Err)
			}
		}
	} else if _, errExecute := exec.Execute(context.Background(), auth, req, opts); errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	if tools := gjson.GetBytes(gotBody, "tools"); !tools.Exists() || len(tools.Array()) != 0 {
		t.Fatalf("upstream tools = %s, want empty after suppression; body=%s", tools.Raw, gotBody)
	}
	if gjson.GetBytes(gotBody, "tool_choice").Exists() {
		t.Fatalf("upstream tool_choice was not removed: %s", gotBody)
	}
	if gjson.GetBytes(gotBody, "parallel_tool_calls").Exists() {
		t.Fatalf("upstream parallel_tool_calls should be removed with empty tools: %s", gotBody)
	}
}

func TestEnsureImageGenerationTool_DisabledProviderDropsEmptyNestedContainers(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		cliproxyauth.AttributeCodexDisableImageGeneration: "true",
	}}
	body := []byte(`{"model":"gpt-5.6-sol","tools":[{"type":"namespace","name":"outer","tools":[{"type":"namespace","name":"empty","tools":[]},{"type":"image_generation"}]}],"tool_choice":"required","parallel_tool_calls":true}`)

	result := ensureImageGenerationTool(body, "gpt-5.6-sol", auth, nil)
	if tools := gjson.GetBytes(result, "tools"); !tools.Exists() || len(tools.Array()) != 0 {
		t.Fatalf("tools = %s, want empty after nested containers are pruned", tools.Raw)
	}
	if gjson.GetBytes(result, "tool_choice").Exists() {
		t.Fatalf("tool_choice should be removed after only empty containers remain: %s", result)
	}
	if gjson.GetBytes(result, "parallel_tool_calls").Exists() {
		t.Fatalf("parallel_tool_calls should be removed after only empty containers remain: %s", result)
	}
}

func TestEnsureImageGenerationTool_DisabledProviderPreservesCustomImageGenerationFunctionChoice(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		cliproxyauth.AttributeCodexDisableImageGeneration: "true",
	}}
	body := []byte(`{"model":"gpt-5.6-sol","tools":[{"type":"function","name":"image_generation"}],"tool_choice":{"type":"function","name":"image_generation"}}`)

	result := ensureImageGenerationTool(body, "gpt-5.6-sol", auth, nil)
	if !gjson.GetBytes(result, "tool_choice").Exists() {
		t.Fatalf("custom function tool_choice was removed: %s", result)
	}
}

func TestEnsureImageGenerationTool_DisabledProviderRemovesRootChoiceWhenRootToolsEmpty(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		cliproxyauth.AttributeCodexDisableImageGeneration: "true",
	}}
	body := []byte(`{"model":"gpt-5.6-sol","tools":[{"type":"image_generation"}],"input":[{"type":"message","tools":[{"type":"function","name":"keep_input_tool"}]}],"tool_choice":"required","parallel_tool_calls":true}`)

	result := ensureImageGenerationTool(body, "gpt-5.6-sol", auth, nil)
	if tools := gjson.GetBytes(result, "tools"); !tools.Exists() || len(tools.Array()) != 0 {
		t.Fatalf("root tools = %s, want empty array", tools.Raw)
	}
	if gjson.GetBytes(result, "tool_choice").Exists() {
		t.Fatalf("root tool_choice should be removed when root tools are empty: %s", result)
	}
	if tools := gjson.GetBytes(result, "input.0.tools"); !tools.Exists() || len(tools.Array()) != 1 {
		t.Fatalf("input tools = %s, want preserved unrelated tool", tools.Raw)
	}
	if !gjson.GetBytes(result, "parallel_tool_calls").Bool() {
		t.Fatalf("parallel_tool_calls should remain while an input tool exists: %s", result)
	}
}

func TestEnsureImageGenerationTool_DisabledProviderRemovesRootAdditionalTools(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		cliproxyauth.AttributeCodexDisableImageGeneration: "true",
	}}
	body := []byte(`{"model":"gpt-5.6-sol","additional_tools":[{"type":"image_generation"}],"tool_choice":"required","parallel_tool_calls":true}`)

	result := ensureImageGenerationTool(body, "gpt-5.6-sol", auth, nil)
	tools := gjson.GetBytes(result, "additional_tools")
	if !tools.Exists() || len(tools.Array()) != 0 {
		t.Fatalf("additional_tools = %s, want empty array", tools.Raw)
	}
	if gjson.GetBytes(result, "tool_choice").Exists() {
		t.Fatalf("tool_choice should be removed after root additional_tools are stripped: %s", result)
	}
	if gjson.GetBytes(result, "parallel_tool_calls").Exists() {
		t.Fatalf("parallel_tool_calls should be removed after root additional_tools are stripped: %s", result)
	}
}

func TestEnsureImageGenerationTool_DisabledProviderRemovesToolsFromAnyInputItem(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		cliproxyauth.AttributeCodexDisableImageGeneration: "true",
	}}
	body := []byte(`{"model":"gpt-5.6-sol","input":[{"type":"message","tools":[{"type":"image_generation"}]}],"tool_choice":"required","parallel_tool_calls":true}`)

	result := ensureImageGenerationTool(body, "gpt-5.6-sol", auth, nil)
	tools := gjson.GetBytes(result, "input.0.tools")
	if !tools.Exists() || len(tools.Array()) != 0 {
		t.Fatalf("input tools = %s, want empty array", tools.Raw)
	}
	if gjson.GetBytes(result, "tool_choice").Exists() {
		t.Fatalf("tool_choice should be removed after input tools are stripped: %s", result)
	}
}

func TestEnsureImageGenerationTool_DisabledProviderPrunesNestedAllowedToolChoices(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		cliproxyauth.AttributeCodexDisableImageGeneration: "true",
	}}
	body := []byte(`{"model":"gpt-5.6-sol","tools":[{"type":"function","name":"keep_me"}],"tool_choice":{"type":"allowed_tools","tools":[{"type":"namespace","name":"outer","tools":[{"type":"function","name":"image_gen.imagegen"},{"type":"function","name":"keep_choice"}]}]}}`)

	result := ensureImageGenerationTool(body, "gpt-5.6-sol", auth, nil)
	choices := gjson.GetBytes(result, "tool_choice.tools.0.tools").Array()
	if len(choices) != 1 || choices[0].Get("name").String() != "keep_choice" {
		t.Fatalf("nested allowed tools = %s, want only keep_choice", gjson.GetBytes(result, "tool_choice.tools").Raw)
	}
}

func TestEnsureImageGenerationTool_DisabledProviderRemovesNamespacedFunctionReference(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		cliproxyauth.AttributeCodexDisableImageGeneration: "true",
	}}
	body := []byte(`{"model":"gpt-5.6-sol","tools":[{"type":"function","name":"imagegen","namespace":"image_gen"},{"type":"function","name":"keep_me"}],"tool_choice":{"type":"function","name":"imagegen","namespace":"image_gen"}}`)

	result := ensureImageGenerationTool(body, "gpt-5.6-sol", auth, nil)
	tools := gjson.GetBytes(result, "tools").Array()
	if len(tools) != 1 || tools[0].Get("name").String() != "keep_me" {
		t.Fatalf("tools = %s, want only keep_me", gjson.GetBytes(result, "tools").Raw)
	}
	if gjson.GetBytes(result, "tool_choice").Exists() {
		t.Fatalf("namespaced image tool_choice was not removed: %s", result)
	}
}

func TestEnsureImageGenerationTool_DisabledProviderPrunesAllowedToolChoices(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		cliproxyauth.AttributeCodexDisableImageGeneration: "true",
	}}
	body := []byte(`{"model":"gpt-5.6-sol","tools":[{"type":"image_generation"},{"type":"function","name":"keep_me"}],"tool_choice":{"type":"allowed_tools","tools":[{"type":"image_generation"},{"type":"function","name":"imagegen","namespace":"image_gen"},{"type":"function","name":"keep_me"}]}}`)

	result := ensureImageGenerationTool(body, "gpt-5.6-sol", auth, nil)
	choices := gjson.GetBytes(result, "tool_choice.tools").Array()
	if len(choices) != 1 || choices[0].Get("name").String() != "keep_me" {
		t.Fatalf("tool_choice.tools = %s, want only keep_me", gjson.GetBytes(result, "tool_choice.tools").Raw)
	}
}

func TestEnsureImageGenerationTool_EnabledProviderKeepsHistoricalRecognitionScope(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","tools":[{"type":"function","function":{"name":"image_gen.imagegen"}}]}`)

	result := ensureImageGenerationTool(body, "gpt-5.6-sol", nil, nil)
	tools := gjson.GetBytes(result, "tools").Array()
	if len(tools) != 2 {
		t.Fatalf("tools = %s, want historical ImageGen injection alongside nested function shape", gjson.GetBytes(result, "tools").Raw)
	}
	if got := tools[1].Get("type").String(); got != "image_generation" {
		t.Fatalf("injected tool type = %q, want image_generation; tools=%s", got, gjson.GetBytes(result, "tools").Raw)
	}
}

func TestEnsureImageGenerationTool_DisabledProviderStripsAdditionalToolsAndNestedNamespaces(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		cliproxyauth.AttributeCodexDisableImageGeneration: "true",
	}}
	body := []byte(`{"model":"gpt-5.6-sol","input":[{"type":"additional_tools","role":"developer","tools":[{"type":"namespace","name":"outer","tools":[{"type":"namespace","name":"image_gen","tools":[{"type":"function","name":"imagegen"}]},{"type":"function","name":"keep_me"}]}]}],"tool_choice":{"type":"function","name":"imagegen","namespace":"image_gen"},"parallel_tool_calls":true}`)

	result := ensureImageGenerationTool(body, "gpt-5.6-sol", auth, nil)
	nested := gjson.GetBytes(result, "input.0.tools.0.tools").Array()
	if len(nested) != 1 || nested[0].Get("name").String() != "keep_me" {
		t.Fatalf("additional_tools nested tools = %s, want only keep_me", gjson.GetBytes(result, "input.0.tools.0.tools").Raw)
	}
	if gjson.GetBytes(result, "tool_choice").Exists() {
		t.Fatalf("image tool_choice was not removed: %s", result)
	}
	if !gjson.GetBytes(result, "parallel_tool_calls").Bool() {
		t.Fatalf("parallel_tool_calls should remain while a nested input tool exists: %s", result)
	}
}
