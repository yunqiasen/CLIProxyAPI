package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// ModelCatalogPolicy controls discovery only, never request eligibility.
type ModelCatalogPolicy struct {
	Order  string   `yaml:"order,omitempty" json:"order"`
	Pinned []string `yaml:"pinned,omitempty" json:"pinned"`
	Hidden []string `yaml:"hidden,omitempty" json:"hidden"`
}

// Normalized returns an independently owned policy with explicit defaults.
func (p ModelCatalogPolicy) Normalized() (ModelCatalogPolicy, error) {
	p.Order = strings.ToLower(strings.TrimSpace(p.Order))
	if p.Order == "" {
		p.Order = "preserve"
	}
	if p.Order != "preserve" && p.Order != "asc" && p.Order != "desc" {
		return ModelCatalogPolicy{}, fmt.Errorf("client.model-catalog.order must be preserve, asc or desc")
	}
	clean := func(values []string) []string {
		out := make([]string, 0, len(values))
		for _, v := range values {
			if v = strings.TrimSpace(v); v != "" {
				out = append(out, v)
			}
		}
		return out
	}
	p.Hidden, p.Pinned = clean(p.Hidden), clean(p.Pinned)
	return p, nil
}

// UnmarshalYAML prevents YAML scalar coercion from accepting malformed rules.
func (p *ModelCatalogPolicy) UnmarshalYAML(node *yaml.Node) error {
	if node.Tag == "!!null" {
		*p = ModelCatalogPolicy{}
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("client.model-catalog must be an object")
	}
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i].Value, node.Content[i+1]
		if value.Tag == "!!null" {
			continue
		}
		switch key {
		case "order":
			if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
				return fmt.Errorf("client.model-catalog.order must be a string")
			}
		case "hidden", "pinned":
			if value.Kind != yaml.SequenceNode {
				return fmt.Errorf("client.model-catalog.%s must be a list of strings", key)
			}
			for _, item := range value.Content {
				if item.Kind != yaml.ScalarNode || item.Tag != "!!str" {
					return fmt.Errorf("client.model-catalog.%s must contain strings", key)
				}
			}
		default:
			return fmt.Errorf("unknown client.model-catalog field %q", key)
		}
	}
	type wire ModelCatalogPolicy
	var decoded wire
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	normalized, err := ModelCatalogPolicy(decoded).Normalized()
	if err != nil {
		return err
	}
	*p = normalized
	return nil
}
