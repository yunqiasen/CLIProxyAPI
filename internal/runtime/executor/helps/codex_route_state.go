package helps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	auth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type routeStateOwner struct {
	route   string
	expires time.Time
}

var codexRouteOwners = struct {
	sync.Mutex
	values map[string]routeStateOwner
}{values: make(map[string]routeStateOwner)}

// CodexRouteState tracks opaque reasoning items, not a mutable session-wide route.
// Concurrent completions therefore cannot retire each other's valid history.
type CodexRouteState struct{ scope, route string }

func routeStateDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// PrepareCodexRouteState changes only reasoning already observed on a different
// route. Unknown state, compaction and stored-response references are preserved.
func PrepareCodexRouteState(credential *auth.Auth, model, scope string, body []byte) ([]byte, *CodexRouteState) {
	state := &CodexRouteState{scope: scope}
	if credential == nil || scope == "" {
		return body, state
	}
	identity, _ := json.Marshal(struct {
		ID, Provider, Model string
		Attributes          map[string]string
		Metadata            map[string]any
	}{credential.ID, credential.Provider, model, credential.Attributes, map[string]any{"access_token": credential.Metadata["access_token"], "account_id": credential.Metadata["account_id"]}})
	state.route = routeStateDigest(string(identity))
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, state
	}
	now := time.Now()
	changed := false
	items := make([]json.RawMessage, 0, len(input.Array()))
	codexRouteOwners.Lock()
	defer codexRouteOwners.Unlock()
	for _, item := range input.Array() {
		raw := []byte(item.Raw)
		if item.Get("type").String() == "reasoning" {
			encrypted := item.Get("encrypted_content")
			if encrypted.Type == gjson.String && encrypted.String() != "" {
				key := routeStateDigest(scope + "\x00" + encrypted.String())
				owner, known := codexRouteOwners.values[key]
				if known && now.Before(owner.expires) && owner.route != state.route {
					raw, _ = sjson.DeleteBytes(raw, "encrypted_content")
					raw, _ = sjson.DeleteBytes(raw, "id")
					changed = true
					// Empty reasoning shells are not portable input; retain readable content.
					if len(gjson.GetBytes(raw, "summary").Array()) == 0 && len(gjson.GetBytes(raw, "content").Array()) == 0 {
						continue
					}
				}
			}
		}
		items = append(items, raw)
	}
	if changed {
		if encoded, err := json.Marshal(items); err == nil {
			if updated, errSet := sjson.SetRawBytes(body, "input", encoded); errSet == nil {
				body = updated
			}
		}
	}
	return body, state
}

// Completed commits ownership only for actual successful output, never for an
// attempted route or an input supplied by a client.
func (s *CodexRouteState) Completed(ctx context.Context, event []byte) {
	if (ctx != nil && ctx.Err() != nil) || s == nil || s.scope == "" || s.route == "" || gjson.GetBytes(event, "type").String() != "response.completed" {
		return
	}
	status := gjson.GetBytes(event, "response.status").String()
	if status != "" && status != "completed" {
		return
	}
	now := time.Now()
	codexRouteOwners.Lock()
	defer codexRouteOwners.Unlock()
	for key, owner := range codexRouteOwners.values {
		if now.After(owner.expires) {
			delete(codexRouteOwners.values, key)
		}
	}
	for _, item := range gjson.GetBytes(event, "response.output").Array() {
		if item.Get("type").String() != "reasoning" || item.Get("encrypted_content").Type != gjson.String || item.Get("encrypted_content").String() == "" {
			continue
		}
		key := routeStateDigest(s.scope + "\x00" + item.Get("encrypted_content").String())
		if len(codexRouteOwners.values) >= 4096 {
			for victim := range codexRouteOwners.values {
				delete(codexRouteOwners.values, victim)
				break
			}
		}
		codexRouteOwners.values[key] = routeStateOwner{route: s.route, expires: now.Add(30 * time.Minute)}
	}
}
