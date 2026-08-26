// Package contracts exposes the embedded cross-language machine contract.
package contracts

import (
	"embed"
	"fmt"
)

//go:embed v2/*.json v2/fixtures/*/*.json revision/v2/*.json revision/v2/fixtures/*/*.json
var files embed.FS

var schemaFiles = map[string]string{
	"plugin":   "v2/cowork-plugin.schema.json",
	"relation": "v2/cowork-plugin-relation.schema.json",
	"manifest": "v2/cowork-plugin-manifest.schema.json",
	"package":  "v2/cowork-plugin-package.schema.json",
	"revision": "revision/v2/cowork-plugin-revision-content.schema.json",
	"errors":   "v2/errors.json",
}

func Schema(name string) ([]byte, error) {
	path, ok := schemaFiles[name]
	if !ok {
		return nil, fmt.Errorf("unknown contract %q", name)
	}
	return files.ReadFile(path)
}

func Fixture(path string) ([]byte, error) {
	return files.ReadFile("v2/fixtures/" + path)
}

func RevisionFixture(path string) ([]byte, error) {
	return files.ReadFile("revision/v2/fixtures/" + path)
}
