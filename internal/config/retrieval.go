package config

import (
	"fmt"
	"strings"
)

// ValidateOpenAICompatibilityModels rejects ambiguous non-chat model settings
// before loading or saving them. An empty type retains legacy chat/image behavior.
func ValidateOpenAICompatibilityModels(providers []OpenAICompatibility) error {
	for i, provider := range providers {
		for j, model := range provider.Models {
			field := fmt.Sprintf("openai-compatibility[%d].models[%d]", i, j)
			kind := strings.ToLower(strings.TrimSpace(model.Type))
			switch kind {
			case "":
				if strings.TrimSpace(model.UpstreamPath) != "" {
					return fmt.Errorf("%s.upstream-path requires type: embeddings or rerank", field)
				}
			case "embeddings", "rerank":
				if model.Image {
					return fmt.Errorf("%s: image and retrieval type are mutually exclusive", field)
				}
			default:
				return fmt.Errorf("%s.type must be empty, embeddings, or rerank", field)
			}
		}
	}
	return nil
}
