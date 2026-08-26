package plugin_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-plugin-lib/contracts"
	"github.com/Mininglamp-OSS/octo-plugin-lib/plugin"
)

func TestValidContractFixtures(t *testing.T) {
	for _, name := range []string{
		"expert.json", "skill.json", "expert-team.json", "connector-mcp.json",
		"connector-cli.json", "connector-skill-only.json", "connector-openconnector.json",
	} {
		t.Run(name, func(t *testing.T) {
			data, err := contracts.Fixture("valid/" + name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := plugin.DecodePlugin(data); err != nil {
				t.Fatalf("DecodePlugin() error = %v", err)
			}
		})
	}

	data, err := contracts.Fixture("valid/relation.json")
	if err != nil {
		t.Fatal(err)
	}
	relation, err := plugin.DecodeRelation(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := plugin.ValidateRelationEndpoints(relation, plugin.TypeExpert, plugin.TypeSkill); err != nil {
		t.Fatalf("ValidateRelationEndpoints() error = %v", err)
	}
}

func TestPluginStatusContract(t *testing.T) {
	if plugin.StatusActive != "ACTIVE" || plugin.StatusArchived != "ARCHIVED" {
		t.Fatalf("status values = ACTIVE(%q), ARCHIVED(%q)", plugin.StatusActive, plugin.StatusArchived)
	}
	if !plugin.IsValidStatus(plugin.StatusActive) || !plugin.IsActiveStatus(plugin.StatusActive) {
		t.Fatal("ACTIVE must be valid and active")
	}
	if !plugin.IsValidStatus(plugin.StatusArchived) || plugin.IsActiveStatus(plugin.StatusArchived) {
		t.Fatal("ARCHIVED must be valid and inactive")
	}
	if err := plugin.ValidateStatus(plugin.Status("DISABLED")); err == nil {
		t.Fatal("ValidateStatus() accepted an unknown state")
	}

	data, err := contracts.Fixture("invalid/plugin-status.json")
	if err != nil {
		t.Fatal(err)
	}
	var item plugin.Plugin
	if err := json.Unmarshal(data, &item); err != nil {
		t.Fatal(err)
	}
	if err := plugin.ValidatePlugin(item); err == nil {
		t.Fatal("ValidatePlugin() accepted an unknown status")
	}
}

func TestMCPConnectorRequiresDescriptorButNotSkill(t *testing.T) {
	data, err := contracts.Fixture("valid/connector-mcp.json")
	if err != nil {
		t.Fatal(err)
	}
	var item plugin.Plugin
	if err := json.Unmarshal(data, &item); err != nil {
		t.Fatal(err)
	}
	packageValue, err := plugin.DecodePackage(plugin.TypeConnector, item.PluginJSON)
	if err != nil {
		t.Fatalf("DecodePackage() rejected an MCP connector without a Skill: %v", err)
	}
	if len(packageValue.Attachments) != 1 || packageValue.Attachments[0].Path != "mcp.json" {
		t.Fatalf("fixture must contain only mcp.json, got %#v", packageValue.Attachments)
	}

	withoutDescriptor := json.RawMessage(`{"$schema":"cowork-plugin-package-2.0.json","connector":{"type":"mcp","source":"connector.example"},"attachments":[]}`)
	if _, err := plugin.DecodePackage(plugin.TypeConnector, withoutDescriptor); err == nil {
		t.Fatal("DecodePackage() accepted an MCP connector without mcp.json")
	}
}

func TestPublicFieldsDoNotDrift(t *testing.T) {
	tests := []struct {
		name       string
		typeOf     reflect.Type
		schemaName string
		want       []string
	}{
		{"plugin", reflect.TypeOf(plugin.Plugin{}), "plugin", []string{"created_at", "manifest_json", "plugin_hash", "plugin_id", "plugin_json", "plugin_name", "plugin_type", "status", "updated_at"}},
		{"relation", reflect.TypeOf(plugin.Relation{}), "relation", []string{"relation_type", "source_plugin_id", "target_plugin_id"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := jsonTags(test.typeOf); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("Go fields = %v, want %v", got, test.want)
			}
			data, err := contracts.Schema(test.schemaName)
			if err != nil {
				t.Fatal(err)
			}
			var schema struct {
				Properties map[string]json.RawMessage `json:"properties"`
			}
			if err := json.Unmarshal(data, &schema); err != nil {
				t.Fatal(err)
			}
			got := make([]string, 0, len(schema.Properties))
			for field := range schema.Properties {
				got = append(got, field)
			}
			sort.Strings(got)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("Schema fields = %v, want %v", got, test.want)
			}
			html, err := os.ReadFile("../docs/octo-plugin-lib-trd.html")
			if err != nil {
				t.Fatal(err)
			}
			for _, field := range test.want {
				if !strings.Contains(string(html), "<code>"+field+"</code>") {
					t.Errorf("HTML TRD does not mention %s", field)
				}
			}
		})
	}
}

func TestSchemaIDsAndErrorCodesDoNotDrift(t *testing.T) {
	for name, want := range map[string]string{
		"plugin":   plugin.PluginSchemaID,
		"manifest": plugin.ManifestSchemaID,
		"package":  plugin.PackageSchemaID,
		"relation": plugin.RelationSchemaID,
		"revision": plugin.RevisionContentSchemaID,
	} {
		data, err := contracts.Schema(name)
		if err != nil {
			t.Fatal(err)
		}
		var document struct {
			ID string `json:"$id"`
		}
		if err := json.Unmarshal(data, &document); err != nil || document.ID != want {
			t.Fatalf("%s schema ID = %q, want %q: %v", name, document.ID, want, err)
		}
	}

	data, err := contracts.Schema("errors")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Codes []plugin.ErrorCode `json:"codes"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	want := []plugin.ErrorCode{
		plugin.CodeInvalidJSON, plugin.CodeInvalidField, plugin.CodeInvalidPluginType,
		plugin.CodeInvalidRelationType, plugin.CodeInvalidRelation,
		plugin.CodeInvalidAttachmentPath, plugin.CodeDuplicateAttachmentPath,
		plugin.CodeInvalidAttachmentContent, plugin.CodeHashMismatch,
	}
	if !reflect.DeepEqual(document.Codes, want) {
		t.Fatalf("contract error codes = %v, want %v", document.Codes, want)
	}
}

func jsonTags(value reflect.Type) []string {
	fields := make([]string, 0, value.NumField())
	for index := 0; index < value.NumField(); index++ {
		field := strings.Split(value.Field(index).Tag.Get("json"), ",")[0]
		if field != "" && field != "-" {
			fields = append(fields, field)
		}
	}
	sort.Strings(fields)
	return fields
}

func TestPackageRejectsUnsafePath(t *testing.T) {
	for _, name := range []string{"parent-path.json", "control-path.json", "trailing-slash-path.json", "windows-drive-path.json"} {
		t.Run(name, func(t *testing.T) {
			data, err := contracts.Fixture("invalid/" + name)
			if err != nil {
				t.Fatal(err)
			}
			_, err = plugin.DecodePackage(plugin.TypeExpert, data)
			var violation *plugin.ValidationError
			if !errors.As(err, &violation) || violation.Code != plugin.CodeInvalidAttachmentPath {
				t.Fatalf("DecodePackage() error = %v", err)
			}
		})
	}
}

func TestPackageRejectsFieldsOutsideTheMachineContract(t *testing.T) {
	for _, test := range []struct {
		name string
		code plugin.ErrorCode
	}{
		{name: "connector-null.json", code: plugin.CodeInvalidField},
		{name: "content-size-overflow.json", code: plugin.CodeInvalidAttachmentContent},
		{name: "duplicate-path.json", code: plugin.CodeDuplicateAttachmentPath},
		{name: "empty-mime-part.json", code: plugin.CodeInvalidField},
		{name: "null-content-hash.json", code: plugin.CodeInvalidField},
		{name: "null-content-size.json", code: plugin.CodeInvalidField},
		{name: "mime-type.json", code: plugin.CodeInvalidField},
		{name: "path-too-long.json", code: plugin.CodeInvalidAttachmentPath},
		{name: "raw-derived-metadata.json", code: plugin.CodeInvalidAttachmentContent},
		{name: "unicode-whitespace-mime-type.json", code: plugin.CodeInvalidField},
		{name: "storage-hash-format.json", code: plugin.CodeInvalidField},
		{name: "storage-uri.json", code: plugin.CodeInvalidJSON},
		{name: "unknown-package-field.json", code: plugin.CodeInvalidJSON},
	} {
		t.Run(test.name, func(t *testing.T) {
			data, err := contracts.Fixture("invalid/" + test.name)
			if err != nil {
				t.Fatal(err)
			}
			_, err = plugin.DecodePackage(plugin.TypeSkill, data)
			var violation *plugin.ValidationError
			if !errors.As(err, &violation) || violation.Code != test.code {
				t.Fatalf("DecodePackage() error = %v, want %s", err, test.code)
			}
		})
	}
}

func TestUnicodeWhitespaceSemanticsMatchSchema(t *testing.T) {
	manifest, err := contracts.Fixture("invalid/unicode-whitespace-manifest-name.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.DecodeManifest(manifest); err == nil {
		t.Fatal("DecodeManifest() accepted a name containing only Unicode White_Space")
	}

	pluginJSON, err := contracts.Fixture("invalid/unicode-whitespace-plugin-name.json")
	if err != nil {
		t.Fatal(err)
	}
	var item plugin.Plugin
	if err := json.Unmarshal(pluginJSON, &item); err != nil {
		t.Fatal(err)
	}
	if err := plugin.ValidatePlugin(item); err == nil {
		t.Fatal("ValidatePlugin() accepted a plugin_name containing only Unicode White_Space")
	}

	for _, test := range []struct {
		name       string
		pluginType plugin.Type
	}{
		{name: "unicode-whitespace-connector-source.json", pluginType: plugin.TypeConnector},
	} {
		data, err := contracts.Fixture("invalid/" + test.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := plugin.DecodePackage(test.pluginType, data); err == nil {
			t.Fatalf("DecodePackage() accepted %s", test.name)
		}
	}
}

func TestCanonicalJSON(t *testing.T) {
	data, err := contracts.Fixture("golden/canonical-json.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		MaxNesting              int `json:"max_nesting"`
		MaxNumberCharacters     int `json:"max_number_characters"`
		MaxTotalNumberExpansion int `json:"max_total_number_expansion"`
		Cases                   []struct {
			Name      string `json:"name"`
			Input     string `json:"input"`
			Canonical string `json:"canonical"`
		} `json:"cases"`
		InvalidInputs []string `json:"invalid_inputs"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Cases) == 0 || len(fixture.InvalidInputs) == 0 {
		t.Fatal("Canonical JSON fixture cases must not be empty")
	}
	for _, test := range fixture.Cases {
		t.Run(test.Name, func(t *testing.T) {
			got, err := plugin.CanonicalJSON([]byte(test.Input))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != test.Canonical {
				t.Fatalf("CanonicalJSON() = %s, want %s", got, test.Canonical)
			}
		})
	}
	if _, err := plugin.CanonicalJSON([]byte(`{"a":1,"a":2}`)); err == nil {
		t.Fatal("CanonicalJSON() accepted duplicate keys")
	}
	if _, err := plugin.CanonicalJSON([]byte{'{', '"', 0xff, '"', ':', '1', '}'}); err == nil {
		t.Fatal("CanonicalJSON() accepted invalid UTF-8")
	}
	for _, input := range fixture.InvalidInputs {
		if _, err := plugin.CanonicalJSON([]byte(input)); err == nil {
			t.Fatalf("CanonicalJSON() accepted invalid input: %s", input)
		}
	}
	if got, err := plugin.CanonicalJSON([]byte(`{"text":"\ud834\udd1e"}`)); err != nil || string(got) != `{"text":"𝄞"}` {
		t.Fatalf("CanonicalJSON() surrogate pair = %s, %v", got, err)
	}
	expanded, err := plugin.CanonicalJSON([]byte(`{"number":1e10000}`))
	if err != nil {
		t.Fatalf("CanonicalJSON() rejected maximum exponent: %v", err)
	}
	canonicalAgain, err := plugin.CanonicalJSON(expanded)
	if err != nil || !bytes.Equal(canonicalAgain, expanded) {
		t.Fatalf("CanonicalJSON() is not idempotent for an expanded number: %v", err)
	}
	tooLong := []byte(`{"number":` + strings.Repeat("1", 10_241) + `}`)
	if _, err := plugin.CanonicalJSON(tooLong); err == nil {
		t.Fatal("CanonicalJSON() accepted a number longer than its bounded representation")
	}
	if fixture.MaxNumberCharacters != 10_240 {
		t.Fatalf("canonical fixture max_number_characters = %d, want 10240", fixture.MaxNumberCharacters)
	}
	if fixture.MaxTotalNumberExpansion != 10_240 {
		t.Fatalf("canonical fixture max_total_number_expansion = %d, want 10240", fixture.MaxTotalNumberExpansion)
	}
	if fixture.MaxNesting != 512 {
		t.Fatalf("canonical fixture max_nesting = %d, want 512", fixture.MaxNesting)
	}
	valid := []byte(strings.Repeat("[", fixture.MaxNesting) + "0" + strings.Repeat("]", fixture.MaxNesting))
	if _, err := plugin.CanonicalJSON(valid); err != nil {
		t.Fatalf("CanonicalJSON() rejected max_nesting containers: %v", err)
	}
	invalid := []byte(strings.Repeat("[", fixture.MaxNesting+1) + "0" + strings.Repeat("]", fixture.MaxNesting+1))
	if _, err := plugin.CanonicalJSON(invalid); err == nil {
		t.Fatal("CanonicalJSON() accepted more than max_nesting containers")
	}
}

func TestStrictPublicDecodersRejectUnknownFields(t *testing.T) {
	valid, err := contracts.Fixture("valid/skill.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(valid, &document); err != nil {
		t.Fatal(err)
	}
	document["unknown"] = true
	invalid, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.DecodePlugin(invalid); err == nil {
		t.Fatal("DecodePlugin() accepted an unknown field")
	}

	relation, err := contracts.Fixture("invalid/relation-unknown-field.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.DecodeRelation(relation); err == nil {
		t.Fatal("DecodeRelation() accepted an unknown field")
	}
}

func TestGoldenHash(t *testing.T) {
	data, err := contracts.Fixture("golden/plugin-hash.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Name     string          `json:"name"`
			Manifest json.RawMessage `json:"manifest_json"`
			Package  json.RawMessage `json:"plugin_json"`
			Hash     string          `json:"plugin_hash"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Cases) == 0 {
		t.Fatal("Plugin hash fixture cases must not be empty")
	}
	for _, test := range fixture.Cases {
		t.Run(test.Name, func(t *testing.T) {
			got, err := plugin.ComputePluginHash(test.Manifest, test.Package)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.Hash {
				t.Fatalf("ComputePluginHash() = %s, want %s", got, test.Hash)
			}
		})
	}
}

func TestPluginHashUsesContentIdentityAndIgnoresAttachmentOrder(t *testing.T) {
	manifest := []byte(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"Skill","plugin_type":"skill","name":"Skill","description":""}`)
	first := []byte(`{"$schema":"cowork-plugin-package-2.0.json","attachments":[{"path":"SKILL.md","content_type":"storage","mime_type":"text/markdown","content_size":7,"content_hash":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},{"path":"README.md","content_type":"raw","mime_type":"text/markdown","raw_content":"readme"}]}`)
	changed := []byte(`{"$schema":"cowork-plugin-package-2.0.json","attachments":[{"path":"SKILL.md","content_type":"storage","mime_type":"text/markdown","content_size":8,"content_hash":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},{"path":"README.md","content_type":"raw","mime_type":"text/markdown","raw_content":"readme"}]}`)
	reordered := []byte(`{"$schema":"cowork-plugin-package-2.0.json","attachments":[{"path":"README.md","content_type":"raw","mime_type":"text/markdown","raw_content":"readme"},{"path":"SKILL.md","content_type":"storage","mime_type":"text/markdown","content_size":7,"content_hash":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}`)

	firstHash, err := plugin.ComputePluginHash(manifest, first)
	if err != nil {
		t.Fatal(err)
	}
	changedHash, err := plugin.ComputePluginHash(manifest, changed)
	if err != nil {
		t.Fatal(err)
	}
	if changedHash == firstHash {
		t.Fatal("changing storage content identity did not change plugin hash")
	}
	reorderedHash, err := plugin.ComputePluginHash(manifest, reordered)
	if err != nil {
		t.Fatal(err)
	}
	if reorderedHash != firstHash {
		t.Fatal("attachment order changed plugin hash")
	}
}

func TestInvalidAttachmentShapeReturnsErrorInsteadOfPanicking(t *testing.T) {
	manifest := []byte(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"Skill","plugin_type":"skill","name":"Skill","description":""}`)
	for _, packageJSON := range [][]byte{
		[]byte(`{"$schema":"cowork-plugin-package-2.0.json","attachments":[null]}`),
		[]byte(`{"$schema":"cowork-plugin-package-2.0.json","attachments":["SKILL.md"]}`),
	} {
		if _, err := plugin.NormalizeRevisionContent(plugin.RevisionContent{
			PluginType: plugin.TypeSkill, ManifestJSON: manifest, PluginJSON: packageJSON,
		}); err != nil {
			continue
		}
		t.Fatalf("ComputePluginHash(%s) accepted an invalid attachment", packageJSON)
	}
}

func TestPackageAcceptsSchemaValidExponentInteger(t *testing.T) {
	data, err := contracts.Fixture("valid/storage-exponent-size.json")
	if err != nil {
		t.Fatal(err)
	}
	packageValue, err := plugin.DecodePackage(plugin.TypeSkill, data)
	if err != nil {
		t.Fatalf("DecodePackage() rejected a JSON Schema integer: %v", err)
	}
	if got := *packageValue.Attachments[0].ContentSize; got != 1000 {
		t.Fatalf("content_size = %d, want 1000", got)
	}
}

func TestNormalizePlugin(t *testing.T) {
	now := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
	item := plugin.Plugin{
		PluginID: "018f7f0a-9c2b-7c31-a101-8b5d7b2fabcd", PluginName: "Skill", PluginType: plugin.TypeSkill,
		ManifestJSON: json.RawMessage(`{"plugin_type":"skill","name":"Skill","description":"","plugin_name":"Skill","$schema":"cowork-plugin-manifest-2.0.json","extension":{"enabled":true}}`),
		PluginJSON:   json.RawMessage(`{"attachments":[{"path":"SKILL.md","content_type":"raw","mime_type":"text/markdown","raw_content":"# Skill"}],"$schema":"cowork-plugin-package-2.0.json"}`),
		PluginHash:   "sha256:" + strings.Repeat("0", 64),
		Status:       plugin.StatusActive,
		CreatedAt:    now, UpdatedAt: now,
	}
	normalized, err := plugin.NormalizePlugin(item)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix([]byte(normalized.PluginHash), []byte("sha256:")) || len(normalized.PluginHash) != 71 {
		t.Fatalf("unexpected plugin hash %q", normalized.PluginHash)
	}
	if normalized.PluginHash == item.PluginHash {
		t.Fatal("NormalizePlugin() did not replace the caller-provided hash")
	}
	if !bytes.Contains(normalized.ManifestJSON, []byte(`"extension":{"enabled":true}`)) {
		t.Fatalf("NormalizePlugin() lost Manifest extensions: %s", normalized.ManifestJSON)
	}

	normalized.PluginName = "Different"
	if err := plugin.ValidatePlugin(normalized); err == nil {
		t.Fatal("ValidatePlugin() accepted a Plugin/Manifest name mismatch")
	}
}

func TestPluginJSONMustBeExplicit(t *testing.T) {
	now := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
	item := plugin.Plugin{
		PluginID: "018f7f0a-9c2b-7c31-a101-8b5d7b2fabcd", PluginName: "Skill", PluginType: plugin.TypeSkill,
		ManifestJSON: json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"Skill","plugin_type":"skill","name":"Skill","description":""}`),
		Status:       plugin.StatusActive,
		CreatedAt:    now, UpdatedAt: now,
	}
	if err := plugin.ValidatePlugin(item); err == nil {
		t.Fatal("ValidatePlugin() accepted a missing plugin_json value")
	}
	if _, err := plugin.NormalizePlugin(item); err == nil {
		t.Fatal("NormalizePlugin() accepted a missing plugin_json value")
	}
	for _, pluginType := range []plugin.Type{
		plugin.TypeExpert, plugin.TypeSkill, plugin.TypeExpertTeam, plugin.TypeConnector,
	} {
		item.PluginType = pluginType
		item.PluginName = string(pluginType)
		item.ManifestJSON = json.RawMessage(fmt.Sprintf(
			`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":%q,"plugin_type":%q,"name":%q,"description":""}`,
			pluginType, pluginType, pluginType,
		))
		item.PluginJSON = json.RawMessage("null")
		if _, err := plugin.NormalizePlugin(item); err != nil {
			t.Fatalf("NormalizePlugin() rejected %s with explicit null: %v", pluginType, err)
		}
	}
}

func TestManifestDescriptionMustBeExplicit(t *testing.T) {
	_, err := plugin.DecodeManifest(json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"Skill","plugin_type":"skill","name":"Skill"}`))
	var violation *plugin.ValidationError
	if !errors.As(err, &violation) || violation.Code != plugin.CodeInvalidField || violation.Path != "manifest_json.description" {
		t.Fatalf("DecodeManifest() error = %v", err)
	}
	if _, err := plugin.DecodeManifest(json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"Skill","plugin_type":"skill","name":"Skill","description":""}`)); err != nil {
		t.Fatalf("DecodeManifest() rejected explicit empty description: %v", err)
	}
	if _, err := plugin.DecodeManifest(json.RawMessage(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"Skill","plugin_type":"skill","name":"Skill","description":null}`)); err == nil {
		t.Fatal("DecodeManifest() accepted a null description")
	}
}

func TestRelationValidationOrderIsStable(t *testing.T) {
	err := plugin.ValidateRelation(plugin.Relation{})
	var violation *plugin.ValidationError
	if !errors.As(err, &violation) || violation.Code != plugin.CodeInvalidField || violation.Path != "source_plugin_id" {
		t.Fatalf("ValidateRelation() error = %v", err)
	}
}

func TestDocumentedSemanticRules(t *testing.T) {
	relation := plugin.Relation{
		SourcePluginID: "018f7f0a-9c2b-7c31-a101-8b5d7b2f1111",
		TargetPluginID: "018f7f0a-9c2b-7c31-a101-8b5d7b2f3333",
	}
	for _, test := range []struct {
		relationType           plugin.RelationType
		sourceType, targetType plugin.Type
	}{
		{plugin.RelationExpertTeamExpert, plugin.TypeExpertTeam, plugin.TypeExpert},
		{plugin.RelationExpertSkill, plugin.TypeExpert, plugin.TypeSkill},
		{plugin.RelationExpertConnector, plugin.TypeExpert, plugin.TypeConnector},
	} {
		t.Run(string(test.relationType), func(t *testing.T) {
			relation.RelationType = test.relationType
			if err := plugin.ValidateRelationEndpoints(relation, test.sourceType, test.targetType); err != nil {
				t.Fatalf("ValidateRelationEndpoints() error = %v", err)
			}
			if err := plugin.ValidateRelationEndpoints(relation, test.targetType, test.sourceType); err == nil {
				t.Fatal("ValidateRelationEndpoints() accepted reversed endpoints")
			}
		})
	}

	relation.RelationType = plugin.RelationExpertSkill
	relation.TargetPluginID = relation.SourcePluginID
	if err := plugin.ValidateRelation(relation); err == nil {
		t.Fatal("ValidateRelation() accepted a self-reference")
	}
	relation.TargetPluginID = "018f7f0a-9c2b-7c31-a101-8b5d7b2f3333"

	duplicatePath := json.RawMessage(`{"$schema":"cowork-plugin-package-2.0.json","attachments":[{"path":"SKILL.md","content_type":"raw","mime_type":"text/markdown","raw_content":"# Skill"},{"path":"SKILL.md","content_type":"raw","mime_type":"text/markdown","raw_content":"duplicate"}]}`)
	if _, err := plugin.DecodePackage(plugin.TypeSkill, duplicatePath); err == nil {
		t.Fatal("DecodePackage() accepted duplicate attachment paths")
	}

	wrongSize := json.RawMessage(`{"$schema":"cowork-plugin-package-2.0.json","attachments":[{"path":"SKILL.md","content_type":"raw","mime_type":"text/markdown","raw_content":"# Skill","content_size":1}]}`)
	if _, err := plugin.DecodePackage(plugin.TypeSkill, wrongSize); err == nil {
		t.Fatal("DecodePackage() accepted a mismatched raw content_size")
	}

	wrongHash := json.RawMessage(`{"$schema":"cowork-plugin-package-2.0.json","attachments":[{"path":"SKILL.md","content_type":"raw","mime_type":"text/markdown","raw_content":"# Skill","content_hash":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}`)
	if _, err := plugin.DecodePackage(plugin.TypeSkill, wrongHash); err == nil {
		t.Fatal("DecodePackage() accepted a mismatched raw content_hash")
	}
}

func TestSemanticInvalidFixtures(t *testing.T) {
	data, err := contracts.Fixture("semantic/invalid.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Name             string           `json:"name"`
			Validator        string           `json:"validator"`
			PluginType       plugin.Type      `json:"plugin_type"`
			SourcePluginType plugin.Type      `json:"source_plugin_type"`
			TargetPluginType plugin.Type      `json:"target_plugin_type"`
			ExpectedCode     plugin.ErrorCode `json:"expected_code"`
			Input            json.RawMessage  `json:"input"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, test := range fixture.Cases {
		t.Run(test.Name, func(t *testing.T) {
			var err error
			switch test.Validator {
			case "plugin":
				_, err = plugin.DecodePlugin(test.Input)
			case "relation":
				_, err = plugin.DecodeRelation(test.Input)
			case "relation_endpoints":
				var relation plugin.Relation
				relation, err = plugin.DecodeRelation(test.Input)
				if err == nil {
					err = plugin.ValidateRelationEndpoints(relation, test.SourcePluginType, test.TargetPluginType)
				}
			case "package":
				_, err = plugin.DecodePackage(test.PluginType, test.Input)
			default:
				t.Fatalf("unknown semantic validator %q", test.Validator)
			}
			var violation *plugin.ValidationError
			if !errors.As(err, &violation) || violation.Code != test.ExpectedCode {
				t.Fatalf("error = %v, want %s", err, test.ExpectedCode)
			}
		})
	}
}

func TestDocumentationMatchesContract(t *testing.T) {
	documents := []string{
		"../README.md",
		"../contracts/v2/README.md",
		"../contracts/revision/v2/README.md",
		"../docs/TRD.md",
		"../docs/er.md",
		"../docs/usage.md",
		"../docs/market-integration.md",
		"../docs/octo-plugin-lib-trd.html",
	}
	for _, filename := range documents {
		data, err := os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		for _, phrase := range []string{"PluginBundle", "SavePluginGraph", "独立 Plugin Service"} {
			if strings.Contains(string(data), phrase) {
				t.Errorf("%s contains historical contract language %q", filename, phrase)
			}
		}
		lower := strings.ToLower(string(data))
		for _, phrase := range []string{"legacy plugin service", "deprecated plugin bundle"} {
			if strings.Contains(lower, phrase) {
				t.Errorf("%s contains historical contract language %q", filename, phrase)
			}
		}
	}

	for _, filename := range []string{"../docs/TRD.md", "../docs/octo-plugin-lib-trd.html"} {
		data, err := os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range []string{
			plugin.PluginSchemaID,
			"cowork-plugin-relation-2.0.json",
			plugin.ManifestSchemaID,
			plugin.PackageSchemaID,
			plugin.RevisionContentSchemaID,
		} {
			if !strings.Contains(string(data), value) {
				t.Errorf("%s does not mention Schema ID %s", filename, value)
			}
		}
		for _, code := range []plugin.ErrorCode{
			plugin.CodeInvalidJSON, plugin.CodeInvalidField, plugin.CodeInvalidPluginType,
			plugin.CodeInvalidRelationType, plugin.CodeInvalidRelation,
			plugin.CodeInvalidAttachmentPath, plugin.CodeDuplicateAttachmentPath,
			plugin.CodeInvalidAttachmentContent, plugin.CodeHashMismatch,
		} {
			if !strings.Contains(string(data), string(code)) {
				t.Errorf("%s does not mention error code %s", filename, code)
			}
		}
		if !strings.Contains(string(data), "9007199254740991") {
			t.Errorf("%s does not document the content_size JavaScript-safe limit", filename)
		}
		for _, phrase := range []string{"数字词法及规范化结果最长 10,240 字符", "累计数字展开增量最多 10,240 字符", "指数范围为 -10000～10000", "O(k log k)", "规范化输出之和同阶"} {
			if !strings.Contains(string(data), phrase) {
				t.Errorf("%s does not document canonical JSON boundary %q", filename, phrase)
			}
		}
		for _, phrase := range []string{"数组顺序", "不读取 storage 对象字节", "不校验 UUID version 或 variant"} {
			if !strings.Contains(string(data), phrase) {
				t.Errorf("%s does not document representation boundary %q", filename, phrase)
			}
		}
		for _, phrase := range []string{"Unicode scalar value", "UTF-16 code-unit", "最多嵌套 512", "SAVEPOINT", "pluginstore.ErrTransactionAborted", "ARCHIVED Plugin"} {
			if !strings.Contains(string(data), phrase) {
				t.Errorf("%s does not document runtime contract %q", filename, phrase)
			}
		}
		for _, name := range []string{
			"contracts.Schema", "contracts.Fixture", "contracts.RevisionFixture", "plugin.DecodePlugin", "plugin.DecodeRelation", "plugin.DecodeRevisionContent", "plugin.DecodeManifest", "plugin.DecodePackage",
			"plugin.ValidatePluginID", "plugin.ValidateStatus", "plugin.ValidatePlugin", "plugin.NormalizePlugin", "plugin.ValidateRelation",
			"plugin.ValidateRelationEndpoints", "plugin.CanonicalJSON", "plugin.ComputePluginHash",
			"plugin.ValidateRevisionContent", "plugin.NormalizeRevisionContent",
			"plugin.Type.Valid", "plugin.IsValidStatus", "plugin.IsActiveStatus", "plugin.RelationType.Valid", "pluginstore.Code",
			"mysqlstore.SchemaSQL", "mysqlstore.Install", "mysqlstore.VerifySchema", "mysqlstore.New", "Store.WithTx",
			"pluginservice.New", "Service.WithTx", "Service.Create", "Service.Get", "Service.List", "Service.Update",
			"Service.SetStatus", "Service.ReplaceRelations", "Service.GetRevision", "Service.ListRevisions", "Service.Restore", "Service.CreateGraph", "pluginconformance.Run",
		} {
			if !strings.Contains(string(data), name) {
				t.Errorf("%s does not mention public API %s", filename, name)
			}
		}
	}

	for _, filename := range []string{"../docs/TRD.md", "../docs/er.md", "../docs/octo-plugin-lib-trd.html"} {
		data, err := os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{
			"plugin_id", "plugin_name", "plugin_type", "manifest_json", "plugin_json", "plugin_hash", "status", "created_at", "updated_at",
			"source_plugin_id", "target_plugin_id", "relation_type",
		} {
			if !strings.Contains(string(data), field) {
				t.Errorf("%s does not mention %s", filename, field)
			}
		}
		for _, value := range []string{
			"expert", "skill", "expert_team", "connector",
			"expert_team_expert", "expert_skill", "expert_connector",
		} {
			if !strings.Contains(string(data), value) {
				t.Errorf("%s does not mention %s", filename, value)
			}
		}
	}

	for _, filename := range []string{"../contracts/v2/README.md", "../docs/TRD.md", "../docs/usage.md", "../docs/octo-plugin-lib-trd.html"} {
		data, err := os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "Windows 盘符路径") {
			t.Errorf("%s does not document the portable attachment path boundary", filename)
		}
	}

	for _, filename := range []string{"../contracts/v2/README.md", "../docs/TRD.md", "../docs/usage.md", "../docs/octo-plugin-lib-trd.html"} {
		data, err := os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "Unicode White_Space") {
			t.Errorf("%s does not document Unicode whitespace semantics", filename)
		}
	}

	for _, filename := range []string{"../contracts/v2/README.md", "../docs/TRD.md", "../docs/octo-plugin-lib-trd.html"} {
		data, err := os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "已发布的同一 Schema ID 内容不可变") {
			t.Errorf("%s does not document Schema ID immutability", filename)
		}
	}

	for _, filename := range []string{"../README.md", "../contracts/v2/README.md", "../docs/TRD.md", "../docs/usage.md", "../docs/octo-plugin-lib-trd.html"} {
		data, err := os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		for _, phrase := range []string{"不等同于", "RFC 8785/JCS", "golden"} {
			if !strings.Contains(string(data), phrase) {
				t.Errorf("%s does not document cross-language canonical JSON boundary %q", filename, phrase)
			}
		}
	}

	for _, filename := range []string{"../README.md", "../contracts/v2/README.md", "../docs/TRD.md", "../docs/er.md", "../docs/octo-plugin-lib-trd.html"} {
		data, err := os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		for _, state := range []string{"ACTIVE", "ARCHIVED"} {
			if !strings.Contains(string(data), state) {
				t.Errorf("%s does not document public Plugin status %s", filename, state)
			}
		}
	}

	for _, filename := range []string{"../README.md", "../docs/TRD.md", "../docs/usage.md", "../docs/octo-plugin-lib-trd.html"} {
		data, err := os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "parseTime=true") || !strings.Contains(string(data), "loc=UTC") {
			t.Errorf("%s does not document the MySQL UTC time requirement", filename)
		}
	}

	html, err := os.ReadFile("../docs/octo-plugin-lib-trd.html")
	if err != nil {
		t.Fatal(err)
	}
	for contract, want := range map[string][]string{
		"plugin":           {"plugin_id", "plugin_name", "plugin_type", "manifest_json", "plugin_json", "plugin_hash", "status", "created_at", "updated_at"},
		"relation":         {"source_plugin_id", "target_plugin_id", "relation_type"},
		"revision-content": {"plugin_type", "manifest_json", "plugin_json", "plugin_hash"},
	} {
		section := regexp.MustCompile(`(?s)<tbody data-contract="` + contract + `">(.*?)</tbody>`).FindSubmatch(html)
		if len(section) != 2 {
			t.Fatalf("HTML TRD has no %s contract table", contract)
		}
		matches := regexp.MustCompile(`data-field="([a-z_]+)"`).FindAllSubmatch(section[1], -1)
		got := make([]string, 0, len(matches))
		for _, match := range matches {
			got = append(got, string(match[1]))
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("HTML %s fields = %v, want %v", contract, got, want)
		}
	}

	apiSection := regexp.MustCompile(`(?s)<tbody data-contract="go-api">(.*?)</tbody>`).FindSubmatch(html)
	if len(apiSection) != 2 {
		t.Fatal("HTML TRD has no Go API table")
	}
	apiMatches := regexp.MustCompile(`<tr><td><code>([^<]+)</code>`).FindAllSubmatch(apiSection[1], -1)
	gotAPIs := make([]string, 0, len(apiMatches))
	for _, match := range apiMatches {
		gotAPIs = append(gotAPIs, string(match[1]))
	}
	wantAPIs := []string{
		"contracts.Schema",
		"contracts.Fixture",
		"contracts.RevisionFixture",
		"plugin.DecodePlugin",
		"plugin.DecodeRelation",
		"plugin.DecodeRevisionContent",
		"plugin.DecodeManifest",
		"plugin.DecodePackage",
		"plugin.ValidatePluginID",
		"plugin.ValidateStatus",
		"plugin.ValidatePlugin",
		"plugin.NormalizePlugin",
		"plugin.ValidateRelation",
		"plugin.ValidateRelationEndpoints",
		"plugin.CanonicalJSON",
		"plugin.ComputePluginHash",
		"plugin.ValidateRevisionContent",
		"plugin.NormalizeRevisionContent",
		"plugin.Type.Valid()",
		"plugin.IsValidStatus",
		"plugin.IsActiveStatus",
		"plugin.RelationType.Valid()",
		"pluginstore.Code",
		"mysqlstore.SchemaSQL",
		"mysqlstore.Install",
		"mysqlstore.VerifySchema",
		"mysqlstore.New",
		"Store.WithTx",
		"pluginservice.New",
		"Service.WithTx",
		"Service.Create",
		"Service.Get",
		"Service.List",
		"Service.Update",
		"Service.SetStatus",
		"Service.ReplaceRelations",
		"Service.GetRevision",
		"Service.ListRevisions",
		"Service.Restore",
		"Service.CreateGraph",
		"pluginconformance.Run",
	}
	if !reflect.DeepEqual(gotAPIs, wantAPIs) {
		t.Errorf("HTML Go APIs = %v, want %v", gotAPIs, wantAPIs)
	}
	for _, rule := range []string{
		`<code>mcp</code></td><td>必须有根目录 <code>mcp.json</code>；嵌套 Skill 可选`,
		`<code>cli</code></td><td>必须有根目录 <code>cli.json</code> 和至少一个 <code>skills/&lt;name&gt;/SKILL.md</code>`,
		`<code>skill-only</code></td><td>必须包含根目录 <code>token-schema.json</code>，并至少包含一个 <code>skills/&lt;name&gt;/SKILL.md</code>；附件顺序不表达业务语义`,
		`<code>openconnector</code></td><td>必须有根目录 <code>mcp.json</code>`,
	} {
		if !strings.Contains(string(html), rule) {
			t.Errorf("HTML Connector rule does not match implementation: %s", rule)
		}
	}
}
