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
