package auth

import "strings"

// ApplyAuthNoteMetadata copies a non-empty note from auth file metadata into the
// auth routing attributes so management surfaces can display it.
func ApplyAuthNoteMetadata(auth *Auth, metadata map[string]any) {
	if auth == nil {
		return
	}
	rawNote, ok := metadata["note"]
	if !ok {
		return
	}
	note, isString := rawNote.(string)
	if !isString {
		return
	}
	trimmed := strings.TrimSpace(note)
	if trimmed == "" {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes["note"] = trimmed
}

// ApplyAuthDisplayNameMetadata copies a non-empty display_name from auth file
// metadata into attributes so management surfaces can show and search it.
func ApplyAuthDisplayNameMetadata(auth *Auth, metadata map[string]any) {
	applyAuthStringAttribute(auth, metadata, "display_name", "display_name")
}

func applyAuthStringAttribute(auth *Auth, metadata map[string]any, metadataKey, attributeKey string) {
	if auth == nil {
		return
	}
	raw, ok := metadata[metadataKey]
	if !ok {
		return
	}
	value, isString := raw.(string)
	if !isString {
		return
	}
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes[attributeKey] = trimmed
}
