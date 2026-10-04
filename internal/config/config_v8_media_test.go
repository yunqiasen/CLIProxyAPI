package config

import (
	"reflect"
	"testing"
)

func TestV8MigrationRetainsMediaProviders(t *testing.T) {
	data := []byte(`port: 8317
request-log-retention-days: 13
media-providers:
  - name: fixture-media
    kind: image
    base-url: https://fixture.invalid
    api-key-entries:
      - api-key: fixture-media-key
    models:
      - name: image-1
        alias: fixture-image
`)
	original, err := ParseConfigBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(original.MediaProviders) != 1 {
		t.Fatalf("fixture invalid: %+v", original.MediaProviders)
	}
	migrated, changed, err := NormalizeConfigLayout(data, true)
	if err != nil || !changed {
		t.Fatalf("migration: changed=%v err=%v", changed, err)
	}
	if err = ValidateV8Config(migrated); err != nil {
		t.Fatal(err)
	}
	after, err := ParseConfigBytes(migrated)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original.MediaProviders, after.MediaProviders) || after.RequestLogRetentionDays != 13 {
		t.Fatalf("fork fields lost: %s", migrated)
	}
}
