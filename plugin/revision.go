package plugin

import (
	"encoding/json"
)

const RevisionContentSchemaID = "cowork-plugin-revision-content-2.0.json"

// RevisionContent is the immutable content shared by host-specific Revision
// records. Revision numbers, current relations, pointers, actors, timestamps,
// transactions, and persistence remain outside the content contract.
type RevisionContent struct {
	PluginType   Type            `json:"plugin_type"`
	ManifestJSON json.RawMessage `json:"manifest_json"`
	PluginJSON   json.RawMessage `json:"plugin_json"`
	PluginHash   string          `json:"plugin_hash"`
}

// DecodeRevisionContent strictly decodes one public Revision Content document
// and applies the semantic invariants that JSON Schema cannot express.
func DecodeRevisionContent(raw json.RawMessage) (RevisionContent, error) {
	var item RevisionContent
	if err := decodeStrict(raw, &item); err != nil {
		return RevisionContent{}, err
	}
	if err := ValidateRevisionContent(item); err != nil {
		return RevisionContent{}, err
	}
	return item, nil
}

// ValidateRevisionContent validates immutable content without modifying the
// caller's value. Relations, authorization, and attachment bytes remain host
// responsibilities.
func ValidateRevisionContent(item RevisionContent) error {
	if !item.PluginType.Valid() {
		return invalid(CodeInvalidPluginType, "plugin_type", "must be expert, skill, expert_team, or connector")
	}
	if err := validateRevisionDocuments(item.PluginType, item.ManifestJSON, item.PluginJSON); err != nil {
		return err
	}
	if !hashPattern.MatchString(item.PluginHash) {
		return invalid(CodeInvalidField, "plugin_hash", "must use sha256:<64 lowercase hex>")
	}
	expectedPluginHash, err := ComputePluginHash(item.ManifestJSON, item.PluginJSON)
	if err != nil {
		return err
	}
	if item.PluginHash != expectedPluginHash {
		return invalid(CodeHashMismatch, "plugin_hash", "does not match manifest_json and plugin_json")
	}
	return nil
}

// NormalizeRevisionContent canonicalizes the Plugin documents, sorts the
// unordered attachment file tree, and recomputes the content hash.
func NormalizeRevisionContent(item RevisionContent) (RevisionContent, error) {
	if !item.PluginType.Valid() {
		return RevisionContent{}, invalid(CodeInvalidPluginType, "plugin_type", "must be expert, skill, expert_team, or connector")
	}
	manifest, err := CanonicalJSON(item.ManifestJSON)
	if err != nil {
		return RevisionContent{}, withPath(err, "manifest_json")
	}
	manifestValue, err := DecodeManifest(manifest)
	if err != nil {
		return RevisionContent{}, err
	}
	if manifestValue.PluginType != item.PluginType {
		return RevisionContent{}, invalid(CodeInvalidField, "manifest_json.plugin_type", "must match plugin_type")
	}
	packageJSON := []byte("null")
	if !isJSONNull(item.PluginJSON) {
		// Validate before sorting so an error path refers to the caller's input.
		if _, err := DecodePackage(item.PluginType, item.PluginJSON); err != nil {
			return RevisionContent{}, err
		}
		packageJSON, err = canonicalPackageJSON(item.PluginJSON)
		if err != nil {
			return RevisionContent{}, withPath(err, "plugin_json")
		}
	}
	item.ManifestJSON = manifest
	item.PluginJSON = packageJSON
	item.PluginHash = computePluginHashCanonical(manifest, packageJSON)
	return item, nil
}

func validateRevisionDocuments(pluginType Type, manifestJSON, pluginJSON json.RawMessage) error {
	manifest, err := DecodeManifest(manifestJSON)
	if err != nil {
		return err
	}
	if manifest.PluginType != pluginType {
		return invalid(CodeInvalidField, "manifest_json.plugin_type", "must match plugin_type")
	}
	if !isJSONNull(pluginJSON) {
		if _, err := DecodePackage(pluginType, pluginJSON); err != nil {
			return err
		}
	}
	return nil
}
