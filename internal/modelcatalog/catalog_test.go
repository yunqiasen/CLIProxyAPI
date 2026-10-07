package modelcatalog_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelcatalog"
)

func TestCatalogPatternsOrderingAndMetadata(t *testing.T) {
	body := []byte(`{"data":[{"id":"zeta","large":9007199254740993},{"id":"old-a"},{"id":"beta","prompt":"<a>&b"},{"id":"alpha"}],"has_more":false,"first_id":"zeta","last_id":"alpha","custom":{"keep":true}}`)
	p := config.ModelCatalogPolicy{Order: "desc", Hidden: []string{"old-*", "zeta"}, Pinned: []string{"alpha", "a*", "old-*"}}
	got := modelcatalog.Transform(body, p)
	var root struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		First string `json:"first_id"`
		Last  string `json:"last_id"`
	}
	if err := json.Unmarshal(got, &root); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, r := range root.Data {
		ids = append(ids, r.ID)
	}
	if !reflect.DeepEqual(ids, []string{"alpha", "beta"}) || root.First != "alpha" || root.Last != "beta" {
		t.Fatalf("got %s", got)
	}
	if !strings.Contains(string(got), `"prompt":"<a>&b"`) || !strings.Contains(string(got), `"keep":true`) {
		t.Fatalf("metadata lost or escaped: %s", got)
	}
}

func TestCatalogFormatsAndStableRules(t *testing.T) {
	tests := []struct {
		name, body string
		p          config.ModelCatalogPolicy
		want       string
	}{
		{"inactive bytes", ` {"data":[{"id":"z"},{"id":"a"}]} `, config.ModelCatalogPolicy{}, ` {"data":[{"id":"z"},{"id":"a"}]} `},
		{"unmatched bytes", ` {"data":[{"id":"z"},{"id":"a"}]} `, config.ModelCatalogPolicy{Hidden: []string{"missing"}}, ` {"data":[{"id":"z"},{"id":"a"}]} `},
		{"all hidden", `{"data":[{"id":"z"}],"has_more":false,"first_id":"z","last_id":"z"}`, config.ModelCatalogPolicy{Hidden: []string{"*"}}, `{"data":[],"first_id":"","has_more":false,"last_id":""}`},
		{"bare Gemini", `{"models":[{"name":"models/gemini-a"},{"name":"models/b"}]}`, config.ModelCatalogPolicy{Hidden: []string{"gemini-*"}}, `{"models":[{"name":"models/b"}]}`},
		{"resource Gemini", `{"models":[{"name":"models/gemini-a"},{"name":"models/b"}]}`, config.ModelCatalogPolicy{Hidden: []string{"models/gemini-*"}}, `{"models":[{"name":"models/b"}]}`},
		{"Codex large metadata", `{"models":[{"slug":"z","prompt":"<x>&","number":9007199254740993},{"slug":"a"}]}`, config.ModelCatalogPolicy{Order: "asc"}, `{"models":[{"slug":"a"},{"slug":"z","prompt":"<x>&","number":9007199254740993}]}`},
		{"pin order", `{"data":[{"id":"c"},{"id":"b"},{"id":"a"}]}`, config.ModelCatalogPolicy{Pinned: []string{"b", "a", "*", "b"}}, `{"data":[{"id":"b"},{"id":"a"},{"id":"c"}]}`},
		{"hidden wins", `{"data":[{"id":"b"},{"id":"a"}]}`, config.ModelCatalogPolicy{Pinned: []string{"*"}, Hidden: []string{"a"}}, `{"data":[{"id":"b"}]}`},
		{"empty", `{"data":[]}`, config.ModelCatalogPolicy{Order: "desc", Hidden: []string{"*"}}, `{"data":[]}`},
		{"malformed", `{"data":[{"id":1}]}`, config.ModelCatalogPolicy{Hidden: []string{"*"}}, `{"data":[{"id":1}]}`},
		{"missing id", `{"data":[{"prompt":"a"}]}`, config.ModelCatalogPolicy{Hidden: []string{"*"}}, `{"data":[{"prompt":"a"}]}`},
		{"unknown payload", `{"choices":[]}`, config.ModelCatalogPolicy{Hidden: []string{"*"}}, `{"choices":[]}`},
		{"not array", `{"data":null}`, config.ModelCatalogPolicy{Hidden: []string{"*"}}, `{"data":null}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := modelcatalog.Transform([]byte(tt.body), tt.p)
			if string(got) != tt.want {
				t.Fatalf("got %s want %s", got, tt.want)
			}
		})
	}
}

func TestCatalogWildcardSemantics(t *testing.T) {
	for _, tt := range []struct {
		pattern, id string
		hidden      bool
	}{
		{"a*a", "a", false}, {"a*a", "aa", true}, {"a**a", "aba", true}, {"*a*b*", "aλb", true}, {"a*b", "ab", true}, {"a*b", "ac", false}, {"*", "你好", true}, {"你*好", "你很好", true}, {"你?好", "你很好", false}, {"A*", "abc", false}, {"*-preview", "m-preview", true}, {"x*y*z", "xyyz", true}, {"x*y*z", "xyzx", false}, {"***", "a", true},
	} {
		t.Run(tt.pattern+"/"+tt.id, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{"data": []any{map[string]any{"id": tt.id}}})
			view, err := modelcatalog.Inspect(body, config.ModelCatalogPolicy{Hidden: []string{tt.pattern}}, "openai")
			if err != nil {
				t.Fatal(err)
			}
			if view.Entries[0].Hidden != tt.hidden {
				t.Fatalf("pattern %q on %q hidden=%v want %v", tt.pattern, tt.id, view.Entries[0].Hidden, tt.hidden)
			}
		})
	}
}

func TestCatalogInventoryUsesSameOrderAndPreservesInput(t *testing.T) {
	body := []byte(`{"models":[{"name":"models/a","displayName":"A"},{"name":"models/b"},{"name":"models/c"}]}`)
	original := string(body)
	p := config.ModelCatalogPolicy{Order: "desc", Pinned: []string{"a", "*"}, Hidden: []string{"models/b"}}
	view, err := modelcatalog.Inspect(body, p, "gemini")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(view.VisibleIDs, []string{"models/a", "models/c"}) || view.Counts.Total != 3 || view.Counts.Hidden != 1 || view.Entries[1].Position != -1 || !reflect.DeepEqual(view.Entries[1].HiddenRules, []string{"models/b"}) {
		t.Fatalf("view=%+v", view)
	}
	if view.Entries[0].Label != "A" || view.Entries[0].Position != 0 {
		t.Fatalf("entry=%+v", view.Entries[0])
	}
	if string(body) != original || p.Order != "desc" {
		t.Fatal("input mutated")
	}
	transformed := modelcatalog.Transform(body, p)
	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(transformed, &result); err != nil {
		t.Fatal(err)
	}
	if result.Models[0].Name != "models/a" || result.Models[1].Name != "models/c" {
		t.Fatalf("output=%s", transformed)
	}
}

func TestCatalogKeepsUnknownEnvelopeFields(t *testing.T) {
	body := []byte(`{"data":[{"id":"a"},{"id":"b"}],"first_id":"custom"}`)
	got := modelcatalog.Transform(body, config.ModelCatalogPolicy{Order: "desc"})
	if string(got) != `{"data":[{"id":"b"},{"id":"a"}],"first_id":"custom"}` {
		t.Fatalf("unknown envelope metadata changed: %s", got)
	}
}
