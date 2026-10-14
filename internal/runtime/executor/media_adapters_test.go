package executor

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	ex "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func adapterExecutorFixture(t *testing.T, adapter, kind, model string, operation config.MediaOperation, handle http.HandlerFunc) (*MediaExecutor, *coreauth.Auth) {
	t.Helper()
	server := httptest.NewServer(handle)
	t.Cleanup(server.Close)
	var provider config.MediaProvider
	raw, _ := json.Marshal(map[string]any{"name": "adapter-fixture", "kind": kind, "base-url": server.URL, "adapter": adapter, "models": []config.MediaModel{{Name: model, Alias: "public"}}, "operations": []config.MediaOperation{operation}})
	if err := json.Unmarshal(raw, &provider); err != nil {
		t.Fatal(err)
	}
	key := util.MediaProviderKey(kind, provider.Name)
	auth := &coreauth.Auth{ID: "adapter-fixture", Provider: key, Attributes: map[string]string{"base_url": server.URL, "media_kind": kind, "media_provider_name": provider.Name, "provider_key": key}}
	return NewMediaExecutor(key, &config.Config{MediaProviders: []config.MediaProvider{provider}}), auth
}

func adapterWAV() []byte {
	data := make([]byte, 48)
	copy(data, "RIFF")
	binary.LittleEndian.PutUint32(data[4:8], 40)
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:20], 16)
	binary.LittleEndian.PutUint16(data[20:22], 1)
	binary.LittleEndian.PutUint16(data[22:24], 1)
	binary.LittleEndian.PutUint32(data[24:28], 8000)
	binary.LittleEndian.PutUint32(data[28:32], 16000)
	binary.LittleEndian.PutUint16(data[32:34], 2)
	binary.LittleEndian.PutUint16(data[34:36], 16)
	copy(data[36:], "data")
	binary.LittleEndian.PutUint32(data[40:44], 4)
	return data
}

func TestMediaAdapterCloudflareGenerateUsesModelProtocol(t *testing.T) {
	for _, model := range []string{"@cf/black-forest-labs/flux-1-schnell", "@cf/black-forest-labs/flux-2-klein-4b"} {
		t.Run(model, func(t *testing.T) {
			op := config.MediaOperation{Name: "generate", Method: "POST", Path: "/{model}", RequestFormat: "json", ModelMode: "required", ResponseFormat: "json-base64", ResultPath: "result.image"}
			executor, auth := adapterExecutorFixture(t, "cloudflare-workers-ai", "image", model, op, func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(model, "schnell") {
					var data map[string]any
					_ = json.NewDecoder(r.Body).Decode(&data)
					if data["steps"] != float64(4) || data["prompt"] != "draw" || len(data) != 2 {
						w.WriteHeader(400)
						io.WriteString(w, `{"error":"expected only prompt and steps"}`)
						return
					}
				} else {
					if err := r.ParseMultipartForm(1 << 20); err != nil || r.FormValue("prompt") != "draw" {
						w.WriteHeader(400)
						io.WriteString(w, `{"error":"expected multipart prompt"}`)
						return
					}
					if r.FormValue("model") != "" {
						t.Error("path model repeated in form")
					}
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"result":{"image":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="}}`)
			})
			resp, err := executor.Execute(t.Context(), auth, ex.Request{Model: "public", Payload: []byte(`{"model":"public","prompt":"draw","num_steps":4,"n":1}`)}, customMediaOptions("generate", "public", "application/json"))
			if err != nil {
				t.Fatal(err)
			}
			if resp.Headers.Get("X-CPA-Media-Result") != "completed" {
				t.Fatalf("result not verified: %s", resp.Payload)
			}
		})
	}
}

func TestMediaAdapterMiMoSpeechTranslatesCanonicalInputAndOutput(t *testing.T) {
	wav := adapterWAV()
	op := config.MediaOperation{Name: "speech", Method: "POST", Path: "/chat/completions", RequestFormat: "json", ModelMode: "required", ResponseFormat: "json-base64", ResultPath: "choices.0.message.audio.data"}
	executor, auth := adapterExecutorFixture(t, "mimo-audio", "audio", "mimo-v2.5-tts", op, func(w http.ResponseWriter, r *http.Request) {
		var data map[string]any
		_ = json.NewDecoder(r.Body).Decode(&data)
		messages, ok := data["messages"].([]any)
		if !ok || len(messages) != 2 || messages[1].(map[string]any)["content"] != "Hello, world" || messages[1].(map[string]any)["role"] != "assistant" {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":"messages is not set"}`)
			return
		}
		if data["model"] != "mimo-v2.5-tts" || data["input"] != nil || data["voice"] != nil {
			t.Errorf("unexpected wire fields: %v", data)
		}
		if data["audio"].(map[string]any)["voice"] != "mimo_default" {
			t.Errorf("missing requested voice")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"audio": map[string]any{"data": base64.StdEncoding.EncodeToString(wav)}}}}})
	})
	resp, err := executor.Execute(t.Context(), auth, ex.Request{Model: "public", Payload: []byte(`{"model":"public","input":"Hello, world","voice":"mimo_default","response_format":"wav","instructions":"Speak clearly"}`)}, customMediaOptions("speech", "public", "application/json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(resp.Payload, wav) || resp.Headers.Get("Content-Type") != "audio/wav" {
		t.Fatalf("expected canonical WAV, got %s bytes=%d", resp.Headers.Get("Content-Type"), len(resp.Payload))
	}
}

func TestMediaAdapterMiMoTranscriptionUsesMultipartFile(t *testing.T) {
	op := config.MediaOperation{Name: "transcribe", Method: "POST", Path: "/chat/completions", RequestFormat: "multipart", ModelMode: "required", ResponseFormat: "passthrough"}
	wav := adapterWAV()
	executor, auth := adapterExecutorFixture(t, "mimo-audio", "audio", "mimo-v2.5-asr", op, func(w http.ResponseWriter, r *http.Request) {
		var data map[string]any
		if json.NewDecoder(r.Body).Decode(&data) != nil {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":"expected JSON input_audio"}`)
			return
		}
		message := data["messages"].([]any)[0].(map[string]any)
		audio := message["content"].([]any)[0].(map[string]any)["input_audio"].(map[string]any)
		if !strings.Contains(audio["data"].(string), base64.StdEncoding.EncodeToString(wav)) {
			t.Error("audio changed")
		}
		if data["asr_options"].(map[string]any)["language"] != "en" {
			t.Error("language lost")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"finish_reason":"stop","message":{"content":"Hello, world"}}]}`)
	})
	var payload bytes.Buffer
	form := multipart.NewWriter(&payload)
	form.WriteField("model", "public")
	form.WriteField("language", "en")
	file, _ := form.CreateFormFile("file", "fixture.wav")
	file.Write(wav)
	form.Close()
	resp, err := executor.Execute(t.Context(), auth, ex.Request{Model: "public", Payload: payload.Bytes()}, customMediaOptions("transcribe", "public", form.FormDataContentType()))
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.Payload) != `{"text":"Hello, world"}` || resp.Headers.Get("X-CPA-Media-Result") != "completed" {
		t.Fatalf("wrong transcription: %s", resp.Payload)
	}
}

func TestMediaAdapterMiMoIncompleteAudioIsNotSuccess(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprint(native), func(t *testing.T) {
			op := config.MediaOperation{Name: "speech", Method: "POST", Path: "/chat/completions", RequestFormat: "json", ModelMode: "required", ResponseFormat: "json-base64", ResultPath: "choices.0.message.audio.data"}
			executor, auth := adapterExecutorFixture(t, "mimo-audio", "audio", "mimo-v2.5-tts", op, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "length", "message": map[string]any{"audio": map[string]any{"data": base64.StdEncoding.EncodeToString(adapterWAV())}}}}})
			})
			input := `{"input":"Hello","response_format":"wav"}`
			if native {
				input = `{"messages":[{"role":"assistant","content":"Hello"}],"audio":{"format":"wav"}}`
			}
			_, err := executor.Execute(t.Context(), auth, ex.Request{Model: "public", Payload: []byte(input)}, customMediaOptions("speech", "public", "application/json"))
			if err == nil {
				t.Fatal("incomplete upstream audio reported as success")
			}
		})
	}
}

func TestMediaExecutorCustomAsyncSuccessIsVerified(t *testing.T) {
	op := config.MediaOperation{Name: "generate", Method: "POST", Path: "/submit", RequestFormat: "json", ModelMode: "required", ResponseFormat: "passthrough", Async: &config.MediaAsyncOperation{TaskIDPath: "task_id", PollMethod: "GET", PollPath: "/tasks/{task_id}", StatusPath: "progress", SuccessValues: []string{"OK"}}}
	executor, auth := adapterExecutorFixture(t, "passthrough", "video", "raw", op, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" {
			io.WriteString(w, `{"task_id":"one"}`)
			return
		}
		io.WriteString(w, `{"task_id":"one","progress":"OK","output":{"url":"https://example.test/video.mp4"}}`)
	})
	resp, err := executor.Execute(t.Context(), auth, ex.Request{Model: "public", Payload: []byte(`{"prompt":"draw"}`)}, customMediaOptions("generate", "public", "application/json"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Headers.Get("X-CPA-Media-Result") != "completed" {
		t.Fatalf("successful task marked %s", resp.Headers.Get("X-CPA-Media-Result"))
	}
}

func TestResolveMediaModelKeepsSuffixCompatibility(t *testing.T) {
	provider := &config.MediaProvider{Prefix: "vendor", Models: []config.MediaModel{{Name: "raw", Alias: "public"}}}
	if got := resolveMediaModel(provider, "vendor/public(high)"); got != "raw" {
		t.Fatalf("got %q want raw", got)
	}
}

func TestMediaAdapterCloudflareEditMapsReferenceImage(t *testing.T) {
	op := config.MediaOperation{Name: "edit", Method: "POST", Path: "/{model}", RequestFormat: "multipart", ModelMode: "required", ResponseFormat: "json-base64", ResultPath: "result.image"}
	image, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	executor, auth := adapterExecutorFixture(t, "cloudflare-workers-ai", "image", "@cf/black-forest-labs/flux-2-klein-4b", op, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
		}
		file, _, err := r.FormFile("input_image_0")
		if err != nil {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":"input_image_0 missing"}`)
			return
		}
		defer file.Close()
		got, _ := io.ReadAll(file)
		if !bytes.Equal(got, image) || r.FormValue("width") != "1024" || r.FormValue("height") != "1024" {
			t.Error("reference or size changed")
		}
		if r.FormValue("model") != "" || r.FormValue("n") != "" {
			t.Error("OpenAI-only fields leaked")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"result": map[string]string{"image": base64.StdEncoding.EncodeToString(image)}})
	})
	var payload bytes.Buffer
	form := multipart.NewWriter(&payload)
	form.WriteField("model", "public")
	form.WriteField("prompt", "Make the circle red")
	form.WriteField("size", "1024x1024")
	form.WriteField("n", "1")
	part, _ := form.CreateFormFile("image", "fixture.png")
	part.Write(image)
	form.Close()
	resp, err := executor.Execute(t.Context(), auth, ex.Request{Model: "public", Payload: payload.Bytes()}, customMediaOptions("edit", "public", form.FormDataContentType()))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Headers.Get("X-CPA-Media-Result") != "completed" {
		t.Fatal("edit not completed")
	}
}

func TestMediaAdapterMiMoNativeTranscriptionPreservesShape(t *testing.T) {
	op := config.MediaOperation{Name: "transcribe", Method: "POST", Path: "/chat/completions", RequestFormat: "multipart", ModelMode: "required", ResponseFormat: "passthrough"}
	output := `{"choices":[{"finish_reason":"stop","message":{"content":"Hello world"}}]}`
	executor, auth := adapterExecutorFixture(t, "mimo-audio", "audio", "mimo-v2.5-asr", op, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			t.Error("native ASR was not JSON")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, output)
	})
	input := `{"messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"Zml4dHVyZQ==","format":"wav"}}]}]}`
	resp, err := executor.Execute(t.Context(), auth, ex.Request{Model: "public", Payload: []byte(input)}, customMediaOptions("transcribe", "public", "application/json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.Payload) != output || resp.Headers.Get("X-CPA-Media-Result") != "completed" {
		t.Fatal("native response changed or unverified")
	}
}

func TestMediaExecutorNormalizedAsyncSuccessKeepsTaskProvenance(t *testing.T) {
	op := config.MediaOperation{
		Name: "generate", Method: "POST", Path: "/submit", RequestFormat: "json", ModelMode: "required",
		ResponseFormat: "json-url", ResultPath: "output.asset",
		Async: &config.MediaAsyncOperation{TaskIDPath: "task_id", PollMethod: "GET", PollPath: "/tasks/{task_id}", StatusPath: "progress", SuccessValues: []string{"OK"}},
	}
	executor, auth := adapterExecutorFixture(t, "passthrough", "video", "raw", op, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" {
			_, _ = io.WriteString(w, `{"task_id":"one"}`)
			return
		}
		_, _ = io.WriteString(w, `{"task_id":"one","progress":"OK","output":{"asset":"https://example.test/video.mp4"}}`)
	})
	resp, err := executor.Execute(t.Context(), auth, ex.Request{Model: "public", Payload: []byte(`{"prompt":"draw"}`)}, customMediaOptions("generate", "public", "application/json"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Metadata[ex.MediaResultMetadataKey] != "completed" || string(resp.Payload) != `{"data":[{"url":"https://example.test/video.mp4"}]}` {
		t.Fatalf("completed normalization changed: metadata=%v payload=%s", resp.Metadata, resp.Payload)
	}
}
