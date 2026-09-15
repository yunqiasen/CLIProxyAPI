package common

import "testing"

func TestCustomToolRestorationRespectsExplicitNamespace(t *testing.T) {
	names := ResponsesCustomToolNames([]byte(`{"tools":[{"type":"custom","name":"apply_patch"}]}`))
	item := []byte(`{"type":"function_call","namespace":"other","name":"apply_patch","arguments":"{\"query\":\"keep\"}"}`)
	if got := RestoreResponsesCustomItem(item, names); string(got) != string(item) {
		t.Fatalf("namespaced function misclassified: %s", got)
	}
}
