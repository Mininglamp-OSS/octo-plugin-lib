package contracts

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"path"
	"testing"

	"github.com/dlclark/regexp2"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type ecmaRegexp regexp2.Regexp

func (re *ecmaRegexp) MatchString(value string) bool {
	matched, err := (*regexp2.Regexp)(re).MatchString(value)
	return err == nil && matched
}

func (re *ecmaRegexp) String() string { return (*regexp2.Regexp)(re).String() }

func compileECMA(pattern string) (jsonschema.Regexp, error) {
	re, err := regexp2.Compile(pattern, regexp2.ECMAScript)
	return (*ecmaRegexp)(re), err
}

func TestSchemasCompileAndFixturesConform(t *testing.T) {
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseRegexpEngine(compileECMA)
	compiler.AssertFormat()

	for _, name := range []string{"manifest", "package", "plugin", "relation", "revision"} {
		data, err := Schema(name)
		if err != nil {
			t.Fatal(err)
		}
		document, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("parse %s schema: %v", name, err)
		}
		var header struct {
			ID string `json:"$id"`
		}
		if err := json.Unmarshal(data, &header); err != nil || header.ID == "" {
			t.Fatalf("%s schema has no $id: %v", name, err)
		}
		if err := compiler.AddResource(header.ID, document); err != nil {
			t.Fatalf("register %s schema: %v", name, err)
		}
	}

	compiled := map[string]*jsonschema.Schema{}
	for _, name := range []string{"manifest", "package", "plugin", "relation", "revision"} {
		data, _ := Schema(name)
		var header struct {
			ID string `json:"$id"`
		}
		_ = json.Unmarshal(data, &header)
		schema, err := compiler.Compile(header.ID)
		if err != nil {
			t.Fatalf("compile %s schema: %v", name, err)
		}
		compiled[name] = schema
	}

	valid, err := fs.Glob(files, "v2/fixtures/valid/*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(valid) == 0 {
		t.Fatal("no embedded valid fixtures found")
	}
	for _, fixturePath := range valid {
		t.Run("valid/"+path.Base(fixturePath), func(t *testing.T) {
			validateFixture(t, compiled[fixtureSchema(t, fixturePath)], fixturePath, true)
		})
	}
	invalid, err := fs.Glob(files, "v2/fixtures/invalid/*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(invalid) == 0 {
		t.Fatal("no embedded invalid fixtures found")
	}
	for _, fixturePath := range invalid {
		t.Run("invalid/"+path.Base(fixturePath), func(t *testing.T) {
			// JSON Schema cannot express uniqueness by Attachment path;
			// every implementation must apply the semantic fixture after Schema validation.
			validBySchema := path.Base(fixturePath) == "duplicate-path.json"
			validateFixture(t, compiled[fixtureSchema(t, fixturePath)], fixturePath, validBySchema)
		})
	}
}

func TestRevisionSchemaFixturesConform(t *testing.T) {
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseRegexpEngine(compileECMA)
	compiler.AssertFormat()

	for _, name := range []string{"manifest", "package", "revision"} {
		data, err := Schema(name)
		if err != nil {
			t.Fatal(err)
		}
		var header struct {
			ID string `json:"$id"`
		}
		if err := json.Unmarshal(data, &header); err != nil {
			t.Fatal(err)
		}
		document, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		if err := compiler.AddResource(header.ID, document); err != nil {
			t.Fatal(err)
		}
	}

	data, err := Schema("revision")
	if err != nil {
		t.Fatal(err)
	}
	var header struct {
		ID string `json:"$id"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(header.ID)
	if err != nil {
		t.Fatal(err)
	}

	valid, err := fs.Glob(files, "revision/v2/fixtures/valid/*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(valid) == 0 {
		t.Fatal("no embedded valid Revision fixtures found")
	}
	for _, path := range valid {
		t.Run(path, func(t *testing.T) { validateFixture(t, schema, path, true) })
	}
	invalid, err := fs.Glob(files, "revision/v2/fixtures/invalid/*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(invalid) == 0 {
		t.Fatal("no embedded invalid Revision fixtures found")
	}
	for _, path := range invalid {
		t.Run(path, func(t *testing.T) { validateFixture(t, schema, path, false) })
	}
}

func validateFixture(t *testing.T, schema *jsonschema.Schema, path string, valid bool) {
	t.Helper()
	data, err := files.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	err = schema.Validate(document)
	if valid && err != nil {
		t.Fatalf("fixture must be valid: %v", err)
	}
	if !valid && err == nil {
		t.Fatal("fixture must be rejected")
	}
}

func fixtureSchema(t *testing.T, fixturePath string) string {
	t.Helper()
	switch path.Base(fixturePath) {
	case "manifest-description-null.json", "manifest-required.json", "unicode-whitespace-manifest-name.json":
		return "manifest"
	case "relation.json", "relation-type.json", "relation-unknown-field.json":
		return "relation"
	case "connector-null.json", "content-size-overflow.json", "control-path.json", "duplicate-path.json", "empty-mime-part.json", "mime-type.json", "null-content-hash.json", "null-content-size.json", "parent-path.json", "path-too-long.json", "raw-derived-metadata.json", "storage-exponent-size.json", "storage-hash-format.json", "storage-uri.json", "trailing-slash-path.json", "unicode-whitespace-connector-source.json", "unicode-whitespace-mime-type.json", "unknown-package-field.json", "windows-drive-path.json":
		return "package"
	case "connector-cli.json", "connector-mcp.json", "connector-openconnector.json", "connector-skill-only.json", "expert-team.json", "expert.json", "skill.json", "plugin-id.json", "plugin-status.json", "unicode-whitespace-plugin-name.json":
		return "plugin"
	default:
		t.Fatalf("fixture %q is not assigned to a schema", fixturePath)
		return ""
	}
}

func TestEmbeddedDocumentsAreJSON(t *testing.T) {
	for name := range schemaFiles {
		data, err := Schema(name)
		if err != nil {
			t.Fatal(err)
		}
		var value any
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestErrorVocabularyMatchesContractMajor(t *testing.T) {
	data, err := Schema("errors")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		ContractVersion string `json:"contract_version"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if document.ContractVersion != "2.0.0" {
		t.Fatalf("errors contract_version = %q, want 2.0.0", document.ContractVersion)
	}
}

func TestUnknownContractIsRejected(t *testing.T) {
	if _, err := Schema("missing"); err == nil {
		t.Fatal("Schema(missing) succeeded")
	}
}

func TestRevisionFixtureIsEmbedded(t *testing.T) {
	if _, err := RevisionFixture("valid/expert-revision.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := RevisionFixture("missing.json"); err == nil {
		t.Fatal("RevisionFixture(missing.json) succeeded")
	}
}
