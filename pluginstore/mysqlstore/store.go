package mysqlstore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	contract "github.com/Mininglamp-OSS/octo-plugin-lib/plugin"
	"github.com/Mininglamp-OSS/octo-plugin-lib/pluginstore"
	driver "github.com/go-sql-driver/mysql"
)

type Store struct {
	db *sql.DB
	tx *sql.Tx
}

var savepointSequence atomic.Uint64

const transactionCleanupTimeout = 5 * time.Second

func New(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("mysqlstore: database is required")
	}
	return &Store{db: db}, nil
}

func (store *Store) WithTx(tx *sql.Tx) (pluginstore.Store, error) {
	if store == nil || store.db == nil || tx == nil {
		return nil, errors.New("mysqlstore: transaction is required")
	}
	return &Store{db: store.db, tx: tx}, nil
}

func (store *Store) Create(ctx context.Context, record pluginstore.CreateRecord) (result pluginstore.Snapshot, err error) {
	err = store.write(ctx, func(tx *sql.Tx) error {
		result, err = create(ctx, tx, record)
		return err
	})
	return result, err
}

func (store *Store) CreateGraph(ctx context.Context, records []pluginstore.CreateRecord) (results []pluginstore.Snapshot, err error) {
	if len(records) == 0 {
		return nil, pluginstore.ErrInvalidArgument
	}
	err = store.write(ctx, func(tx *sql.Tx) error {
		results, err = createGraph(ctx, tx, records)
		return err
	})
	return results, err
}

func (store *Store) UpdateContent(ctx context.Context, record pluginstore.ContentUpdateRecord) (result pluginstore.Snapshot, err error) {
	err = store.write(ctx, func(tx *sql.Tx) error {
		result, err = updateContent(ctx, tx, record)
		return err
	})
	return result, err
}

func (store *Store) SetStatus(ctx context.Context, record pluginstore.StatusUpdateRecord) (result pluginstore.Snapshot, err error) {
	err = store.write(ctx, func(tx *sql.Tx) error {
		result, err = setStatus(ctx, tx, record)
		return err
	})
	return result, err
}

func (store *Store) ReplaceRelations(ctx context.Context, record pluginstore.RelationsUpdateRecord) (result pluginstore.Snapshot, err error) {
	err = store.write(ctx, func(tx *sql.Tx) error {
		result, err = replaceRelations(ctx, tx, record)
		return err
	})
	return result, err
}

func (store *Store) Get(ctx context.Context, scopeID, pluginID string) (pluginstore.Snapshot, error) {
	return get(ctx, store.reader(), scopeID, pluginID)
}

func (store *Store) GetRevision(ctx context.Context, scopeID, pluginID string, revisionNo uint32) (pluginstore.Revision, error) {
	return getRevision(ctx, store.reader(), scopeID, pluginID, revisionNo)
}

func (store *Store) List(ctx context.Context, scopeID string, filter pluginstore.ListFilter) (pluginstore.PluginPage, error) {
	if filter.Page < 1 || filter.PageSize < 1 || filter.PageSize > 100 ||
		filter.Page-1 > math.MaxInt32/filter.PageSize {
		return pluginstore.PluginPage{}, pluginstore.ErrInvalidArgument
	}
	if filter.Status != nil && !contract.IsValidStatus(*filter.Status) {
		return pluginstore.PluginPage{}, pluginstore.ErrInvalidArgument
	}
	from := "FROM plugin p"
	if filter.Query != "" {
		from += ` STRAIGHT_JOIN plugin_revision search_revision
  ON search_revision.scope_id = p.scope_id AND search_revision.plugin_id = p.id
 AND search_revision.revision_no = p.current_revision_no`
	}
	where, arguments := listWhere(scopeID, filter)
	var total int64
	if err := store.reader().QueryRowContext(ctx, "SELECT COUNT(*) "+from+" "+where, arguments...).Scan(&total); err != nil {
		return pluginstore.PluginPage{}, storageError("count Plugins", err)
	}
	arguments = append(arguments, filter.PageSize, (filter.Page-1)*filter.PageSize)
	rows, err := store.reader().QueryContext(ctx, `
SELECT p.id, p.name, p.type, p.status, p.current_revision_no,
       p.lock_version, p.created_at, p.updated_at,
       r.manifest_json, r.plugin_hash
FROM (
  SELECT p.scope_id, p.id, p.name, p.type, p.status,
         p.current_revision_no, p.lock_version, p.created_at, p.updated_at
  `+from+` `+where+`
  ORDER BY p.updated_at DESC, p.id DESC
  LIMIT ? OFFSET ?
) p
JOIN plugin_revision r
  ON r.scope_id = p.scope_id AND r.plugin_id = p.id
 AND r.revision_no = p.current_revision_no
ORDER BY p.updated_at DESC, p.id DESC`, arguments...)
	if err != nil {
		return pluginstore.PluginPage{}, storageError("list Plugins", err)
	}
	defer rows.Close() //nolint:errcheck
	items := make([]pluginstore.PluginSummary, 0, filter.PageSize)
	for rows.Next() {
		var item pluginstore.PluginSummary
		if err := rows.Scan(&item.PluginID, &item.PluginName, &item.PluginType, &item.Status,
			&item.CurrentRevisionNo, &item.LockVersion, &item.CreatedAt, &item.UpdatedAt,
			&item.ManifestJSON, &item.PluginHash); err != nil {
			return pluginstore.PluginPage{}, storageError("scan Plugin list", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return pluginstore.PluginPage{}, storageError("read Plugin list", err)
	}
	return pluginstore.PluginPage{Items: items, Total: total, Page: filter.Page, PageSize: filter.PageSize}, nil
}

func (store *Store) ListRevisions(ctx context.Context, scopeID, pluginID string, page pluginstore.PageRequest) (pluginstore.RevisionPage, error) {
	if page.Page < 1 || page.PageSize < 1 || page.PageSize > 100 ||
		page.Page-1 > math.MaxInt32/page.PageSize {
		return pluginstore.RevisionPage{}, pluginstore.ErrInvalidArgument
	}
	var total int64
	if err := store.reader().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM plugin_revision WHERE scope_id = ? AND plugin_id = ?",
		scopeID, pluginID).Scan(&total); err != nil {
		return pluginstore.RevisionPage{}, storageError("count Revisions", err)
	}
	rows, err := store.reader().QueryContext(ctx, `
SELECT revision_no, plugin_hash, created_by, created_at
FROM plugin_revision
WHERE scope_id = ? AND plugin_id = ?
ORDER BY revision_no DESC
LIMIT ? OFFSET ?`, scopeID, pluginID, page.PageSize, (page.Page-1)*page.PageSize)
	if err != nil {
		return pluginstore.RevisionPage{}, storageError("list Revisions", err)
	}
	defer rows.Close() //nolint:errcheck
	items := make([]pluginstore.RevisionSummary, 0, page.PageSize)
	for rows.Next() {
		var item pluginstore.RevisionSummary
		if err := rows.Scan(&item.RevisionNo, &item.PluginHash, &item.CreatedBy, &item.CreatedAt); err != nil {
			return pluginstore.RevisionPage{}, storageError("scan Revision list", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return pluginstore.RevisionPage{}, storageError("read Revision list", err)
	}
	return pluginstore.RevisionPage{Items: items, Total: total, Page: page.Page, PageSize: page.PageSize}, nil
}

func (store *Store) write(ctx context.Context, operation func(*sql.Tx) error) error {
	if store == nil || store.db == nil {
		return pluginstore.ErrStorage
	}
	if store.tx != nil {
		return writeAtSavepoint(ctx, store.tx, operation)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError("begin transaction", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if err := operation(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return storageError("commit transaction", err)
	}
	return nil
}

func writeAtSavepoint(ctx context.Context, tx *sql.Tx, operation func(*sql.Tx) error) error {
	name := fmt.Sprintf("octo_plugin_lib_%x", savepointSequence.Add(1))
	if _, err := tx.ExecContext(ctx, "SAVEPOINT "+name); err != nil {
		return storageError("create savepoint", err)
	}

	operationErr := operation(tx)
	cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), transactionCleanupTimeout)
	defer cancel()
	if operationErr != nil {
		if _, err := tx.ExecContext(cleanupContext, "ROLLBACK TO SAVEPOINT "+name); err != nil {
			return abortBoundTransaction(tx, errors.Join(operationErr, storageError("rollback to savepoint", err)))
		}
		if _, err := tx.ExecContext(cleanupContext, "RELEASE SAVEPOINT "+name); err != nil {
			return abortBoundTransaction(tx, errors.Join(operationErr, storageError("release savepoint", err)))
		}
		return operationErr
	}
	if _, err := tx.ExecContext(cleanupContext, "RELEASE SAVEPOINT "+name); err != nil {
		return abortBoundTransaction(tx, storageError("release savepoint", err))
	}
	return nil
}

func abortBoundTransaction(tx *sql.Tx, cause error) error {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		cause = errors.Join(cause, storageError("abort caller transaction", err))
	}
	return errors.Join(cause, pluginstore.ErrTransactionAborted)
}

type database interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (store *Store) reader() database {
	if store.tx != nil {
		return store.tx
	}
	return store.db
}

func create(ctx context.Context, tx *sql.Tx, record pluginstore.CreateRecord) (pluginstore.Snapshot, error) {
	if err := validateCreateRecord(record); err != nil {
		return pluginstore.Snapshot{}, err
	}
	locked, err := lockPluginIDs(ctx, tx, record.Plugin.ScopeID, relationTargetIDs(record.Relations))
	if err != nil {
		return pluginstore.Snapshot{}, err
	}
	if err := validateRelations(record.Plugin.PluginType, record.Plugin.Status, nil, record.Relations, locked); err != nil {
		return pluginstore.Snapshot{}, err
	}
	if err := insertPlugin(ctx, tx, record.Plugin); err != nil {
		return pluginstore.Snapshot{}, err
	}
	if err := insertRevision(ctx, tx, record.Revision); err != nil {
		return pluginstore.Snapshot{}, err
	}
	if err := setCurrentRevision(ctx, tx, record.Plugin.ScopeID, record.Plugin.PluginID, 1); err != nil {
		return pluginstore.Snapshot{}, err
	}
	if err := insertRelations(ctx, tx, record.Relations); err != nil {
		return pluginstore.Snapshot{}, err
	}
	return getForUpdate(ctx, tx, record.Plugin.ScopeID, record.Plugin.PluginID)
}

func createGraph(ctx context.Context, tx *sql.Tx, records []pluginstore.CreateRecord) ([]pluginstore.Snapshot, error) {
	ordered := append([]pluginstore.CreateRecord(nil), records...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Plugin.PluginID < ordered[j].Plugin.PluginID })
	known := make(map[string]lockedPlugin, len(ordered))
	var scopeID string
	for _, record := range ordered {
		if err := validateCreateRecord(record); err != nil {
			return nil, err
		}
		if scopeID == "" {
			scopeID = record.Plugin.ScopeID
		}
		if record.Plugin.ScopeID != scopeID {
			return nil, pluginstore.ErrInvalidArgument
		}
		if _, duplicate := known[record.Plugin.PluginID]; duplicate {
			return nil, pluginstore.ErrInvalidArgument
		}
		known[record.Plugin.PluginID] = lockedPlugin{
			pluginType: record.Plugin.PluginType, status: record.Plugin.Status, lockVersion: 1,
		}
	}
	for _, record := range ordered {
		if err := validateRelations(record.Plugin.PluginType, record.Plugin.Status, nil, record.Relations, known); err != nil {
			return nil, err
		}
		for _, relation := range record.Relations {
			if _, exists := known[relation.TargetPluginID]; !exists {
				return nil, fmt.Errorf("%w: graph Relation target is outside the graph", pluginstore.ErrInvalidArgument)
			}
		}
	}
	for _, record := range ordered {
		if err := insertPlugin(ctx, tx, record.Plugin); err != nil {
			return nil, err
		}
	}
	for _, record := range ordered {
		if err := insertRevision(ctx, tx, record.Revision); err != nil {
			return nil, err
		}
		if err := setCurrentRevision(ctx, tx, scopeID, record.Plugin.PluginID, 1); err != nil {
			return nil, err
		}
	}
	for _, record := range ordered {
		if err := insertRelations(ctx, tx, record.Relations); err != nil {
			return nil, err
		}
	}
	results := make([]pluginstore.Snapshot, 0, len(ordered))
	for _, record := range ordered {
		item, err := getForUpdate(ctx, tx, scopeID, record.Plugin.PluginID)
		if err != nil {
			return nil, err
		}
		results = append(results, item)
	}
	return results, nil
}

func updateContent(ctx context.Context, tx *sql.Tx, record pluginstore.ContentUpdateRecord) (pluginstore.Snapshot, error) {
	if err := validateContentUpdateRecord(record); err != nil {
		return pluginstore.Snapshot{}, err
	}
	locked, err := lockPluginIDs(ctx, tx, record.ScopeID, []string{record.PluginID})
	if err != nil {
		return pluginstore.Snapshot{}, err
	}
	currentLock, exists := locked[record.PluginID]
	if !exists {
		return pluginstore.Snapshot{}, pluginstore.ErrNotFound
	}
	if currentLock.lockVersion != record.ExpectedLockVersion {
		return pluginstore.Snapshot{}, pluginstore.ErrConflict
	}
	if currentLock.pluginType != record.PluginType {
		return pluginstore.Snapshot{}, pluginstore.ErrInvalidArgument
	}
	current, err := getForUpdate(ctx, tx, record.ScopeID, record.PluginID)
	if err != nil {
		return pluginstore.Snapshot{}, err
	}
	if !record.ForceRevision && !contract.IsActiveStatus(current.Plugin.Status) {
		return pluginstore.Snapshot{}, fmt.Errorf("%w: ARCHIVED Plugin content cannot be edited", pluginstore.ErrConflict)
	}
	contentChanged := current.Revision.PluginHash != record.Revision.PluginHash
	if !contentChanged && !record.ForceRevision {
		return current, nil
	}
	if current.Plugin.CurrentRevisionNo == math.MaxUint32 || current.Plugin.LockVersion == math.MaxUint32 {
		return pluginstore.Snapshot{}, fmt.Errorf("%w: Plugin version limit reached", pluginstore.ErrConflict)
	}
	record.Revision.ScopeID = record.ScopeID
	record.Revision.PluginID = record.PluginID
	record.Revision.PluginType = record.PluginType
	record.Revision.RevisionNo = current.Plugin.CurrentRevisionNo + 1
	if err := insertRevision(ctx, tx, record.Revision); err != nil {
		return pluginstore.Snapshot{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE plugin
SET name = ?, current_revision_no = ?, lock_version = lock_version + 1, updated_at = ?
WHERE scope_id = ? AND id = ? AND lock_version = ?`,
		record.PluginName, record.Revision.RevisionNo, record.UpdatedAt,
		record.ScopeID, record.PluginID, record.ExpectedLockVersion)
	if err != nil {
		return pluginstore.Snapshot{}, mapWriteError("update Plugin content", err)
	}
	if err := requireOneRow(result); err != nil {
		return pluginstore.Snapshot{}, err
	}
	return getForUpdate(ctx, tx, record.ScopeID, record.PluginID)
}

func setStatus(ctx context.Context, tx *sql.Tx, record pluginstore.StatusUpdateRecord) (pluginstore.Snapshot, error) {
	if record.ScopeID == "" || record.PluginID == "" || record.ExpectedLockVersion == 0 ||
		record.UpdatedAt.IsZero() || !contract.IsValidStatus(record.Status) {
		return pluginstore.Snapshot{}, pluginstore.ErrInvalidArgument
	}
	locked, err := lockPluginIDs(ctx, tx, record.ScopeID, []string{record.PluginID})
	if err != nil {
		return pluginstore.Snapshot{}, err
	}
	current, exists := locked[record.PluginID]
	if !exists {
		return pluginstore.Snapshot{}, pluginstore.ErrNotFound
	}
	if current.lockVersion != record.ExpectedLockVersion {
		return pluginstore.Snapshot{}, pluginstore.ErrConflict
	}
	if current.status == record.Status {
		return getForUpdate(ctx, tx, record.ScopeID, record.PluginID)
	}
	if current.lockVersion == math.MaxUint32 {
		return pluginstore.Snapshot{}, fmt.Errorf("%w: Plugin version limit reached", pluginstore.ErrConflict)
	}
	result, err := tx.ExecContext(ctx, `UPDATE plugin
SET status = ?, lock_version = lock_version + 1, updated_at = ?
WHERE scope_id = ? AND id = ? AND lock_version = ?`,
		record.Status, record.UpdatedAt, record.ScopeID, record.PluginID, record.ExpectedLockVersion)
	if err != nil {
		return pluginstore.Snapshot{}, mapWriteError("update Plugin status", err)
	}
	if err := requireOneRow(result); err != nil {
		return pluginstore.Snapshot{}, err
	}
	return getForUpdate(ctx, tx, record.ScopeID, record.PluginID)
}

func replaceRelations(ctx context.Context, tx *sql.Tx, record pluginstore.RelationsUpdateRecord) (pluginstore.Snapshot, error) {
	if err := validateRelationsUpdateRecord(record); err != nil {
		return pluginstore.Snapshot{}, err
	}
	ids := append([]string{record.PluginID}, relationTargetIDs(record.Relations)...)
	locked, err := lockPluginIDs(ctx, tx, record.ScopeID, ids)
	if err != nil {
		return pluginstore.Snapshot{}, err
	}
	source, exists := locked[record.PluginID]
	if !exists {
		return pluginstore.Snapshot{}, pluginstore.ErrNotFound
	}
	if source.lockVersion != record.ExpectedLockVersion {
		return pluginstore.Snapshot{}, pluginstore.ErrConflict
	}
	current, err := loadRelationsForUpdate(ctx, tx, record.ScopeID, record.PluginID)
	if err != nil {
		return pluginstore.Snapshot{}, err
	}
	if sameRelations(current, record.Relations) {
		return getForUpdate(ctx, tx, record.ScopeID, record.PluginID)
	}
	if source.lockVersion == math.MaxUint32 {
		return pluginstore.Snapshot{}, fmt.Errorf("%w: Plugin version limit reached", pluginstore.ErrConflict)
	}
	if err := validateRelations(source.pluginType, source.status, current, record.Relations, locked); err != nil {
		return pluginstore.Snapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM plugin_relation WHERE scope_id = ? AND source_plugin_id = ?",
		record.ScopeID, record.PluginID); err != nil {
		return pluginstore.Snapshot{}, mapWriteError("replace Relations", err)
	}
	if err := insertRelations(ctx, tx, record.Relations); err != nil {
		return pluginstore.Snapshot{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE plugin
SET lock_version = lock_version + 1, updated_at = ?
WHERE scope_id = ? AND id = ? AND lock_version = ?`,
		record.UpdatedAt, record.ScopeID, record.PluginID, record.ExpectedLockVersion)
	if err != nil {
		return pluginstore.Snapshot{}, mapWriteError("update Plugin Relations", err)
	}
	if err := requireOneRow(result); err != nil {
		return pluginstore.Snapshot{}, err
	}
	return getForUpdate(ctx, tx, record.ScopeID, record.PluginID)
}

func insertPlugin(ctx context.Context, tx *sql.Tx, item pluginstore.Plugin) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO plugin
(scope_id, id, name, type, status, current_revision_no, lock_version, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, NULL, ?, ?, ?)`, item.ScopeID, item.PluginID, item.PluginName,
		item.PluginType, item.Status, item.LockVersion, item.CreatedAt, item.UpdatedAt)
	if err != nil {
		return mapWriteError("insert Plugin", err)
	}
	return nil
}

func insertRevision(ctx context.Context, tx *sql.Tx, revision pluginstore.Revision) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO plugin_revision
(scope_id, plugin_id, revision_no, manifest_json, plugin_json, plugin_hash, created_by, created_at)
VALUES (?, ?, ?, CAST(? AS JSON), CAST(? AS JSON), ?, ?, ?)`,
		revision.ScopeID, revision.PluginID, revision.RevisionNo,
		[]byte(revision.ManifestJSON), []byte(revision.PluginJSON), revision.PluginHash,
		revision.CreatedBy, revision.CreatedAt)
	if err != nil {
		return mapWriteError("insert Revision", err)
	}
	return nil
}

func insertRelations(ctx context.Context, tx *sql.Tx, relations []pluginstore.Relation) error {
	for _, relation := range relations {
		if _, err := tx.ExecContext(ctx, `INSERT INTO plugin_relation
(scope_id, source_plugin_id, relation_type, target_plugin_id)
VALUES (?, ?, ?, ?)`, relation.ScopeID, relation.SourcePluginID,
			relation.RelationType, relation.TargetPluginID); err != nil {
			return mapWriteError("insert Relation", err)
		}
	}
	return nil
}

func setCurrentRevision(ctx context.Context, tx *sql.Tx, scopeID, pluginID string, revisionNo uint32) error {
	result, err := tx.ExecContext(ctx, `UPDATE plugin SET current_revision_no = ?
WHERE scope_id = ? AND id = ? AND current_revision_no IS NULL`, revisionNo, scopeID, pluginID)
	if err != nil {
		return mapWriteError("set current Revision", err)
	}
	return requireOneRow(result)
}

func get(ctx context.Context, db database, scopeID, pluginID string) (pluginstore.Snapshot, error) {
	return getSnapshot(ctx, db, scopeID, pluginID, false)
}

func getForUpdate(ctx context.Context, tx *sql.Tx, scopeID, pluginID string) (pluginstore.Snapshot, error) {
	return getSnapshot(ctx, tx, scopeID, pluginID, true)
}

func getSnapshot(ctx context.Context, db database, scopeID, pluginID string, forUpdate bool) (pluginstore.Snapshot, error) {
	var result pluginstore.Snapshot
	query := `
SELECT p.scope_id, p.id, p.name, p.type, p.status,
       p.current_revision_no, p.lock_version, p.created_at, p.updated_at,
       r.revision_no, r.manifest_json, r.plugin_json, r.plugin_hash, r.created_by, r.created_at
FROM plugin p
JOIN plugin_revision r
  ON r.scope_id = p.scope_id AND r.plugin_id = p.id
 AND r.revision_no = p.current_revision_no
WHERE p.scope_id = ? AND p.id = ?`
	if forUpdate {
		query += " FOR UPDATE"
	}
	row := db.QueryRowContext(ctx, query, scopeID, pluginID)
	if err := row.Scan(&result.Plugin.ScopeID, &result.Plugin.PluginID, &result.Plugin.PluginName,
		&result.Plugin.PluginType, &result.Plugin.Status, &result.Plugin.CurrentRevisionNo,
		&result.Plugin.LockVersion, &result.Plugin.CreatedAt, &result.Plugin.UpdatedAt,
		&result.Revision.RevisionNo, &result.Revision.ManifestJSON, &result.Revision.PluginJSON,
		&result.Revision.PluginHash, &result.Revision.CreatedBy, &result.Revision.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return pluginstore.Snapshot{}, pluginstore.ErrNotFound
		}
		return pluginstore.Snapshot{}, storageError("get Plugin", err)
	}
	result.Revision.ScopeID = result.Plugin.ScopeID
	result.Revision.PluginID = result.Plugin.PluginID
	result.Revision.PluginType = result.Plugin.PluginType
	relations, err := loadRelations(ctx, db, result.Plugin.ScopeID, result.Plugin.PluginID, forUpdate)
	if err != nil {
		return pluginstore.Snapshot{}, err
	}
	result.Relations = relations
	return result, nil
}

func getRevision(ctx context.Context, db database, scopeID, pluginID string, revisionNo uint32) (pluginstore.Revision, error) {
	var result pluginstore.Revision
	row := db.QueryRowContext(ctx, `
SELECT r.scope_id, r.plugin_id, r.revision_no, p.type,
       r.manifest_json, r.plugin_json, r.plugin_hash, r.created_by, r.created_at
FROM plugin_revision r
JOIN plugin p ON p.scope_id = r.scope_id AND p.id = r.plugin_id
WHERE r.scope_id = ? AND r.plugin_id = ? AND r.revision_no = ?`, scopeID, pluginID, revisionNo)
	if err := row.Scan(&result.ScopeID, &result.PluginID, &result.RevisionNo, &result.PluginType,
		&result.ManifestJSON, &result.PluginJSON, &result.PluginHash,
		&result.CreatedBy, &result.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return pluginstore.Revision{}, pluginstore.ErrNotFound
		}
		return pluginstore.Revision{}, storageError("get Revision", err)
	}
	return result, nil
}

func loadRelationsForUpdate(ctx context.Context, tx *sql.Tx, scopeID, pluginID string) ([]pluginstore.Relation, error) {
	return loadRelations(ctx, tx, scopeID, pluginID, true)
}

func loadRelations(ctx context.Context, db database, scopeID, pluginID string, forUpdate bool) ([]pluginstore.Relation, error) {
	query := `SELECT relation_type, target_plugin_id
FROM plugin_relation
WHERE scope_id = ? AND source_plugin_id = ?
ORDER BY relation_type, target_plugin_id`
	if forUpdate {
		query += " FOR UPDATE"
	}
	rows, err := db.QueryContext(ctx, query, scopeID, pluginID)
	if err != nil {
		return nil, storageError("list Relations", err)
	}
	defer rows.Close() //nolint:errcheck
	items := []pluginstore.Relation{}
	for rows.Next() {
		item := pluginstore.Relation{ScopeID: scopeID, SourcePluginID: pluginID}
		if err := rows.Scan(&item.RelationType, &item.TargetPluginID); err != nil {
			return nil, storageError("scan Relation", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, storageError("read Relations", err)
	}
	return items, nil
}

type lockedPlugin struct {
	pluginType  contract.Type
	status      contract.Status
	lockVersion uint32
}

func lockPluginIDs(ctx context.Context, tx *sql.Tx, scopeID string, input []string) (map[string]lockedPlugin, error) {
	ids := append([]string(nil), input...)
	sort.Strings(ids)
	locked := make(map[string]lockedPlugin, len(ids))
	for index, pluginID := range ids {
		if index > 0 && pluginID == ids[index-1] {
			continue
		}
		var item lockedPlugin
		err := tx.QueryRowContext(ctx, `SELECT type, status, lock_version FROM plugin
WHERE scope_id = ? AND id = ? FOR UPDATE`, scopeID, pluginID).
			Scan(&item.pluginType, &item.status, &item.lockVersion)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, storageError("lock Plugin", err)
		}
		locked[pluginID] = item
	}
	return locked, nil
}

func validateRelations(sourceType contract.Type, sourceStatus contract.Status, current, next []pluginstore.Relation, known map[string]lockedPlugin) error {
	existing := make(map[string]struct{}, len(current))
	for _, relation := range current {
		existing[relationKey(relation)] = struct{}{}
	}
	for _, relation := range next {
		target, exists := known[relation.TargetPluginID]
		if !exists {
			return fmt.Errorf("%w: Relation target does not exist", pluginstore.ErrInvalidArgument)
		}
		if err := contract.ValidateRelationEndpoints(contract.Relation{
			SourcePluginID: relation.SourcePluginID, TargetPluginID: relation.TargetPluginID,
			RelationType: relation.RelationType,
		}, sourceType, target.pluginType); err != nil {
			return fmt.Errorf("%w: %v", pluginstore.ErrInvalidArgument, err)
		}
		if _, retained := existing[relationKey(relation)]; !retained &&
			(!contract.IsActiveStatus(sourceStatus) || !contract.IsActiveStatus(target.status)) {
			return fmt.Errorf("%w: new Relations require ACTIVE source and target Plugins", pluginstore.ErrConflict)
		}
	}
	return nil
}

func validateCreateRecord(record pluginstore.CreateRecord) error {
	if record.Plugin.ScopeID == "" || record.Plugin.PluginID == "" || record.Plugin.PluginName == "" ||
		record.Plugin.Status != contract.StatusActive || record.Plugin.CurrentRevisionNo != 1 ||
		record.Plugin.LockVersion != 1 || record.Plugin.CreatedAt.IsZero() || record.Plugin.UpdatedAt.IsZero() ||
		record.Revision.ScopeID != record.Plugin.ScopeID || record.Revision.PluginID != record.Plugin.PluginID ||
		record.Revision.RevisionNo != 1 || record.Revision.PluginType != record.Plugin.PluginType ||
		record.Revision.CreatedAt.IsZero() {
		return pluginstore.ErrInvalidArgument
	}
	if err := contract.ValidatePluginID(record.Plugin.PluginID); err != nil {
		return pluginstore.ErrInvalidArgument
	}
	manifest, err := contract.DecodeManifest(record.Revision.ManifestJSON)
	if err != nil || manifest.PluginName != record.Plugin.PluginName ||
		manifest.PluginType != record.Plugin.PluginType {
		return pluginstore.ErrInvalidArgument
	}
	if err := validateRevisionRecord(record.Revision); err != nil {
		return err
	}
	return validateRelationRows(record.Plugin.ScopeID, record.Plugin.PluginID, record.Relations)
}

func validateContentUpdateRecord(record pluginstore.ContentUpdateRecord) error {
	if record.ScopeID == "" || record.PluginID == "" || record.PluginName == "" ||
		record.ExpectedLockVersion == 0 || record.UpdatedAt.IsZero() {
		return pluginstore.ErrInvalidArgument
	}
	if record.Revision.ScopeID != record.ScopeID || record.Revision.PluginID != record.PluginID ||
		record.Revision.PluginType != record.PluginType || record.Revision.CreatedAt.IsZero() {
		return pluginstore.ErrInvalidArgument
	}
	manifest, err := contract.DecodeManifest(record.Revision.ManifestJSON)
	if err != nil || manifest.PluginName != record.PluginName || manifest.PluginType != record.PluginType {
		return pluginstore.ErrInvalidArgument
	}
	return validateRevisionRecord(record.Revision)
}

func validateRelationsUpdateRecord(record pluginstore.RelationsUpdateRecord) error {
	if record.ScopeID == "" || record.PluginID == "" ||
		record.ExpectedLockVersion == 0 || record.UpdatedAt.IsZero() {
		return pluginstore.ErrInvalidArgument
	}
	return validateRelationRows(record.ScopeID, record.PluginID, record.Relations)
}

func validateRevisionRecord(revision pluginstore.Revision) error {
	if revision.ScopeID == "" || revision.PluginID == "" || revision.CreatedBy == "" ||
		revision.CreatedAt.IsZero() {
		return pluginstore.ErrInvalidArgument
	}
	normalized, err := contract.NormalizeRevisionContent(contract.RevisionContent{
		PluginType: revision.PluginType, ManifestJSON: revision.ManifestJSON,
		PluginJSON: revision.PluginJSON, PluginHash: revision.PluginHash,
	})
	if err != nil || !bytes.Equal(normalized.ManifestJSON, revision.ManifestJSON) ||
		!bytes.Equal(normalized.PluginJSON, revision.PluginJSON) ||
		normalized.PluginHash != revision.PluginHash {
		return pluginstore.ErrInvalidArgument
	}
	return nil
}

func validateRelationRows(scopeID, sourceID string, relations []pluginstore.Relation) error {
	seen := make(map[string]struct{}, len(relations))
	for _, relation := range relations {
		if relation.ScopeID != scopeID || relation.SourcePluginID != sourceID {
			return pluginstore.ErrInvalidArgument
		}
		if err := contract.ValidateRelation(contract.Relation{
			SourcePluginID: sourceID, TargetPluginID: relation.TargetPluginID,
			RelationType: relation.RelationType,
		}); err != nil {
			return pluginstore.ErrInvalidArgument
		}
		key := relationKey(relation)
		if _, duplicate := seen[key]; duplicate {
			return pluginstore.ErrInvalidArgument
		}
		seen[key] = struct{}{}
	}
	return nil
}

func relationTargetIDs(relations []pluginstore.Relation) []string {
	ids := make([]string, 0, len(relations))
	for _, relation := range relations {
		ids = append(ids, relation.TargetPluginID)
	}
	return ids
}

func relationKey(relation pluginstore.Relation) string {
	return string(relation.RelationType) + "\x00" + relation.TargetPluginID
}

func sameRelations(left, right []pluginstore.Relation) bool {
	if len(left) != len(right) {
		return false
	}
	want := make(map[string]struct{}, len(left))
	for _, relation := range left {
		want[relationKey(relation)] = struct{}{}
	}
	for _, relation := range right {
		if _, exists := want[relationKey(relation)]; !exists {
			return false
		}
	}
	return true
}

func listWhere(scopeID string, filter pluginstore.ListFilter) (string, []any) {
	// A NULL pointer is valid only inside the Create transaction before the
	// first Revision is attached. It must never affect a committed API page.
	where := "WHERE p.scope_id = ? AND p.current_revision_no IS NOT NULL"
	arguments := []any{scopeID}
	if filter.PluginType != "" {
		where += " AND p.type = ?"
		arguments = append(arguments, filter.PluginType)
	}
	if filter.Status != nil {
		where += " AND p.status = ?"
		arguments = append(arguments, *filter.Status)
	}
	if filter.Query != "" {
		pattern := "%" + escapeLike(filter.Query) + "%"
		where += ` AND (p.name LIKE ? ESCAPE '=' OR
  JSON_UNQUOTE(JSON_EXTRACT(search_revision.manifest_json, '$.description')) COLLATE utf8mb4_0900_ai_ci LIKE ? ESCAPE '=')`
		arguments = append(arguments, pattern, pattern)
	}
	return where, arguments
}

func escapeLike(value string) string {
	return strings.NewReplacer("=", "==", "%", "=%", "_", "=_").Replace(value)
}

func requireOneRow(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return storageError("inspect affected rows", err)
	}
	if affected != 1 {
		return pluginstore.ErrConflict
	}
	return nil
}

func mapWriteError(operation string, err error) error {
	var mysqlError *driver.MySQLError
	if errors.As(err, &mysqlError) {
		switch mysqlError.Number {
		case 1062:
			return fmt.Errorf("%w: %s", pluginstore.ErrAlreadyExists, operation)
		case 1451, 1452, 3819:
			return fmt.Errorf("%w: %s", pluginstore.ErrIntegrity, operation)
		case 1205, 1213, 1690:
			return fmt.Errorf("%w: %s", pluginstore.ErrConflict, operation)
		}
	}
	return storageError(operation, err)
}

func storageError(operation string, err error) error {
	return fmt.Errorf("%w: %s: %w", pluginstore.ErrStorage, operation, err)
}

var _ pluginstore.Store = (*Store)(nil)
