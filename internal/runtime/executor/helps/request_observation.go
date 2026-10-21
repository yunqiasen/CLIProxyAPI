package helps

import (
	"bytes"
	"context"
	"errors"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/requestlog"
	"github.com/tidwall/gjson"
)

func observeAPIRequest(ctx context.Context, info UpstreamRequestLog) {
	r := requestlog.FromContext(ctx)
	if r == nil {
		return
	}
	provider := info.ProviderName
	if provider == "" {
		provider = info.Provider
	}
	r.BeginAttempt(requestlog.Attempt{Provider: provider, AuthID: info.AuthID, Model: gjson.GetBytes(info.Body, "model").String(), URL: info.URL})
}

func observeAPIResponseChunk(ctx context.Context, payload []byte) {
	r := requestlog.FromContext(ctx)
	if r == nil {
		return
	}
	data := bytes.TrimSpace(payload)
	if len(data) == 0 {
		return
	}
	if bytes.HasPrefix(data, []byte("event:")) {
		if i := bytes.Index(data, []byte("\ndata:")); i >= 0 {
			data = bytes.TrimSpace(data[i+6:])
		}
	} else if bytes.HasPrefix(data, []byte("data:")) {
		data = bytes.TrimSpace(data[5:])
	}
	// Only explicit error envelopes are classified; ordinary model output is not scanned for errors.
	if len(data) > 0 && data[0] == '{' {
		kind := gjson.GetBytes(data, "type").String()
		errorNode := gjson.GetBytes(data, "error")
		if kind == "response.failed" {
			errorNode = gjson.GetBytes(data, "response.error")
		}
		if (errorNode.Exists() && errorNode.Type != gjson.Null) || kind == "error" || kind == "response.failed" {
			diagnostic := errorNode.Raw
			if len(diagnostic) > 32768 {
				diagnostic = diagnostic[:32768]
			}
			if strings.TrimSpace(diagnostic) == "" {
				diagnostic = "upstream response failed"
			}
			r.Failure(errors.New(diagnostic))
			return
		}
	}
	r.Output()
}
