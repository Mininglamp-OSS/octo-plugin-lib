// Package pluginservice implements host-neutral Plugin CRUD use cases.
package pluginservice

import (
	"encoding/json"

	contract "github.com/Mininglamp-OSS/octo-plugin-lib/plugin"
	"github.com/Mininglamp-OSS/octo-plugin-lib/pluginstore"
)

type Scope struct{ ID string }
type Actor struct{ ID string }

type ContentInput struct {
	PluginType   contract.Type
	ManifestJSON json.RawMessage
	PluginJSON   json.RawMessage
}

type RelationInput struct {
	RelationType   contract.RelationType
	TargetPluginID string
}

type CreateInput struct {
	PluginID  string
	Content   ContentInput
	Relations []RelationInput
}

type UpdateInput struct {
	ExpectedLockVersion uint32
	Content             ContentInput
}

type SetStatusInput struct {
	ExpectedLockVersion uint32
	Status              contract.Status
}

type ReplaceRelationsInput struct {
	ExpectedLockVersion uint32
	Relations           []RelationInput
}

type RestoreInput struct {
	ExpectedLockVersion uint32
}

type GraphNodeInput struct {
	PluginID  string
	Content   ContentInput
	Relations []RelationInput
}

type GraphCreateInput struct {
	RootPluginID string
	Nodes        []GraphNodeInput
}

type GraphCreateResult struct {
	Root  pluginstore.Snapshot
	Items []pluginstore.Snapshot
}

type Snapshot = pluginstore.Snapshot
type PluginPage = pluginstore.PluginPage
type Revision = pluginstore.Revision
type RevisionPage = pluginstore.RevisionPage
type ListFilter = pluginstore.ListFilter
type PageRequest = pluginstore.PageRequest
