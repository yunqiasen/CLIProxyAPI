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
