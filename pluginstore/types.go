// Package pluginstore defines the host-neutral Plugin persistence boundary.
package pluginstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	contract "github.com/Mininglamp-OSS/octo-plugin-lib/plugin"
)

type Plugin struct {
	ScopeID           string
	PluginID          string
	PluginName        string
	PluginType        contract.Type
	Status            contract.Status
	CurrentRevisionNo uint32
	LockVersion       uint32
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type Revision struct {
	ScopeID      string
	PluginID     string
	RevisionNo   uint32
	PluginType   contract.Type
	ManifestJSON json.RawMessage
	PluginJSON   json.RawMessage
	PluginHash   string
	CreatedBy    string
	CreatedAt    time.Time
}

// Relation is the source Plugin's current binding. It is intentionally not
// part of immutable Revision history.
type Relation struct {
	ScopeID        string
	SourcePluginID string
	RelationType   contract.RelationType
	TargetPluginID string
}

type Snapshot struct {
	Plugin    Plugin
	Revision  Revision
	Relations []Relation
}

type PluginSummary struct {
	PluginID          string
	PluginName        string
	PluginType        contract.Type
	Status            contract.Status
	CurrentRevisionNo uint32
	LockVersion       uint32
	ManifestJSON      json.RawMessage
	PluginHash        string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type RevisionSummary struct {
	RevisionNo uint32
	PluginHash string
	CreatedBy  string
	CreatedAt  time.Time
}

type ListFilter struct {
	PluginType contract.Type
	Status     *contract.Status
	Query      string
	Page       int
	PageSize   int
}

type PageRequest struct {
	Page     int
	PageSize int
}

type PluginPage struct {
	Items    []PluginSummary
	Total    int64
	Page     int
	PageSize int
}

type RevisionPage struct {
	Items    []RevisionSummary
	Total    int64
	Page     int
	PageSize int
}

type CreateRecord struct {
	Plugin    Plugin
	Revision  Revision
	Relations []Relation
}

type ContentUpdateRecord struct {
	ScopeID             string
	PluginID            string
	PluginName          string
	PluginType          contract.Type
	ExpectedLockVersion uint32
	UpdatedAt           time.Time
	Revision            Revision
	ForceRevision       bool
}

type StatusUpdateRecord struct {
	ScopeID             string
	PluginID            string
	Status              contract.Status
	ExpectedLockVersion uint32
	UpdatedAt           time.Time
}

type RelationsUpdateRecord struct {
	ScopeID             string
	PluginID            string
	Relations           []Relation
	ExpectedLockVersion uint32
	UpdatedAt           time.Time
}

// Store owns atomic persistence. WithTx must receive a transaction from the
// same database/schema used to construct the Store. A transaction-bound Store
// uses a savepoint per call and leaves commit/rollback to the caller. If
// savepoint recovery fails, the Store rolls back the caller's transaction and
// returns an error matching ErrTransactionAborted rather than leaving partial
// Plugin writes.
type Store interface {
	WithTx(*sql.Tx) (Store, error)
	Create(context.Context, CreateRecord) (Snapshot, error)
	CreateGraph(context.Context, []CreateRecord) ([]Snapshot, error)
	Get(context.Context, string, string) (Snapshot, error)
	List(context.Context, string, ListFilter) (PluginPage, error)
	UpdateContent(context.Context, ContentUpdateRecord) (Snapshot, error)
	SetStatus(context.Context, StatusUpdateRecord) (Snapshot, error)
	ReplaceRelations(context.Context, RelationsUpdateRecord) (Snapshot, error)
	GetRevision(context.Context, string, string, uint32) (Revision, error)
	ListRevisions(context.Context, string, string, PageRequest) (RevisionPage, error)
}
