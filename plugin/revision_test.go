package plugin_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"testing"

	"github.com/Mininglamp-OSS/octo-plugin-lib/contracts"
	"github.com/Mininglamp-OSS/octo-plugin-lib/plugin"
)

func TestValidRevisionFixtures(t *testing.T) {
	for _, name := range []string{
		"expert-revision.json",
		"expert-team-revision.json",
		"skill-revision.json",
		"connector-revision.json",
	} {
		t.Run(name, func(t *testing.T) {
			data, err := contracts.RevisionFixture("valid/" + name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := plugin.DecodeRevisionContent(data); err != nil {
				t.Fatalf("DecodeRevisionContent() error = %v", err)
			}
		})
	}
}

func TestDecodeRevisionContentRejectsUnknownFields(t *testing.T) {
	data, err := contracts.RevisionFixture("invalid/relations-field.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.DecodeRevisionContent(data); err == nil {
		t.Fatal("DecodeRevisionContent() accepted an unknown relations field")
	}
}

func TestNormalizeRevisionContentCanonicalizesAndHashes(t *testing.T) {
	item := readRevisionFixture(t, "valid/skill-revision.json")
	item.PluginHash = ""
	originalManifest := append([]byte(nil), item.ManifestJSON...)

	normalized, err := plugin.NormalizeRevisionContent(item)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.PluginHash == "" {
		t.Fatal("NormalizeRevisionContent() did not calculate plugin_hash")
	}
	if err := plugin.ValidateRevisionContent(normalized); err != nil {
		t.Fatalf("normalized content is invalid: %v", err)
	}
	if string(item.ManifestJSON) != string(originalManifest) {
		t.Fatal("NormalizeRevisionContent() modified the caller's input")
	}
}

func TestNormalizeRevisionContentPreservesInputErrorIndex(t *testing.T) {
	_, err := plugin.NormalizeRevisionContent(plugin.RevisionContent{
		PluginType:   plugin.TypeSkill,
		ManifestJSON: json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"Skill","plugin_type":"skill","name":"Skill","description":""}`),
		PluginJSON:   json.RawMessage(`{"$schema":"cowork-plugin-package-2.0.json","attachments":[{"path":"SKILL.md","content_type":"raw","mime_type":"text/markdown","raw_content":"# Skill"},{"path":"A.txt","content_type":"raw","mime_type":"text/plain"}]}`),
	})
	var validationError *plugin.ValidationError
	if !errors.As(err, &validationError) || validationError.Path != "plugin_json.attachments[1]" {
		t.Fatalf("NormalizeRevisionContent() error = %v", err)
	}
}

func TestRevisionValidationRejectsHashMismatch(t *testing.T) {
	item := readRevisionFixture(t, "valid/skill-revision.json")
	item.PluginHash = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	err := plugin.ValidateRevisionContent(item)
	var violation *plugin.ValidationError
	if !errors.As(err, &violation) || violation.Code != plugin.CodeHashMismatch || violation.Path != "plugin_hash" {
		t.Fatalf("ValidateRevisionContent() error = %v", err)
	}
}

func TestRevisionContractFieldsDoNotDrift(t *testing.T) {
	data, err := contracts.Schema("revision")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		ID         string `json:"$id"`
		Properties map[string]json.RawMessage
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.ID != plugin.RevisionContentSchemaID {
		t.Fatalf("revision schema ID = %q, want %q", schema.ID, plugin.RevisionContentSchemaID)
	}
	want := []string{"manifest_json", "plugin_hash", "plugin_json", "plugin_type"}
	if got := revisionSortedKeys(schema.Properties); !reflect.DeepEqual(got, want) {
		t.Fatalf("RevisionContent Schema fields = %v, want %v", got, want)
	}
	if got := jsonTags(reflect.TypeOf(plugin.RevisionContent{})); !reflect.DeepEqual(got, want) {
		t.Fatalf("RevisionContent Go fields = %v, want %v", got, want)
	}
}

func readRevisionFixture(t *testing.T, path string) plugin.RevisionContent {
	t.Helper()
	data, err := contracts.RevisionFixture(path)
	if err != nil {
		t.Fatal(err)
	}
	var item plugin.RevisionContent
	if err := json.Unmarshal(data, &item); err != nil {
		t.Fatal(err)
	}
	return item
}

func revisionSortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
