package mysqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"testing"
	"time"

	contract "github.com/Mininglamp-OSS/octo-plugin-lib/plugin"
	"github.com/Mininglamp-OSS/octo-plugin-lib/pluginconformance"
	"github.com/Mininglamp-OSS/octo-plugin-lib/pluginservice"
	"github.com/Mininglamp-OSS/octo-plugin-lib/pluginstore"
	driver "github.com/go-sql-driver/mysql"
)

func TestMySQLStore(t *testing.T) {
	db := integrationDatabase(t, "OCTO_PLUGIN_LIB_MYSQL_DSN")
	if err := Install(context.Background(), db); err != nil {
		fingerprint, _ := schemaFingerprint(context.Background(), db)
		t.Fatalf("Install: %v\nactual fingerprint:\n%s", err, fingerprint)
	}
	if err := Install(context.Background(), db); err != nil {
		t.Fatalf("repeat Install: %v", err)
	}
	store, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	service, err := pluginservice.New(store)
	if err != nil {
		t.Fatal(err)
	}
	pluginconformance.Run(t, service)
}

func TestHostTransactionRollback(t *testing.T) {
	db := integrationDatabase(t, "OCTO_PLUGIN_LIB_MYSQL_DSN")
	if err := Install(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	store, _ := New(db)
	service, _ := pluginservice.New(store)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS plugin_host_tx_probe (
request_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY
) ENGINE=InnoDB`); err != nil {
		t.Fatal(err)
	}
	requestID := fmt.Sprintf("request-%d", time.Now().UnixNano())
	if _, err := db.Exec("INSERT INTO plugin_host_tx_probe (request_id) VALUES (?)", requestID); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := service.WithTx(tx)
	if err != nil {
		t.Fatal(err)
	}
	scopeID := fmt.Sprintf("tx-%d", time.Now().UnixNano())
	const pluginID = "10000000-0000-4000-8000-000000000001"
	_, err = bound.Create(context.Background(), pluginservice.Scope{ID: scopeID}, pluginservice.Actor{ID: "actor-test"}, pluginservice.CreateInput{
		PluginID: pluginID, Content: rawSkill("Transaction Skill", "rollback"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("INSERT INTO plugin_host_tx_probe (request_id) VALUES (?)", requestID); err == nil {
		t.Fatal("host write unexpectedly succeeded")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(context.Background(), pluginservice.Scope{ID: scopeID}, pluginID); !errors.Is(err, pluginstore.ErrNotFound) {
		t.Fatalf("rolled-back Plugin Get error = %v", err)
	}
}

func TestConcurrentWritesUseCompareAndSwap(t *testing.T) {
	db := integrationDatabase(t, "OCTO_PLUGIN_LIB_MYSQL_DSN")
	if err := Install(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	store, _ := New(db)
	service, _ := pluginservice.New(store)
	scope := pluginservice.Scope{ID: fmt.Sprintf("cas-%d", time.Now().UnixNano())}
	actor := pluginservice.Actor{ID: "actor-test"}
	const pluginID = "20000000-0000-4000-8000-000000000001"
	if _, err := service.Create(context.Background(), scope, actor, pluginservice.CreateInput{
		PluginID: pluginID, Content: rawSkill("CAS Skill", "one"),
	}); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, description := range []string{"two", "three"} {
		go func(description string) {
			<-start
			_, err := service.Update(context.Background(), scope, actor, pluginID, pluginservice.UpdateInput{
				ExpectedLockVersion: 1, Content: rawSkill("CAS Skill", description),
			})
			results <- err
		}(description)
	}
	close(start)
	succeeded, conflicted := 0, 0
	for range 2 {
		switch err := <-results; {
		case err == nil:
			succeeded++
		case errors.Is(err, pluginstore.ErrConflict):
			conflicted++
		default:
			t.Fatalf("concurrent Update error = %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent Update: success=%d conflict=%d", succeeded, conflicted)
	}
}

func TestContentStatusAndRelationsShareOneCompareAndSwap(t *testing.T) {
	db := integrationDatabase(t, "OCTO_PLUGIN_LIB_MYSQL_DSN")
	if err := Install(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	store, _ := New(db)
	service, _ := pluginservice.New(store)
	scope := pluginservice.Scope{ID: fmt.Sprintf("mixed-cas-%d", time.Now().UnixNano())}
	actor := pluginservice.Actor{ID: "actor-test"}
	const sourceID = "21000000-0000-4000-8000-000000000001"
	const targetID = "21000000-0000-4000-8000-000000000002"
	if _, err := service.Create(context.Background(), scope, actor, pluginservice.CreateInput{
		PluginID: targetID, Content: rawSkill("CAS Target", "target"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(context.Background(), scope, actor, pluginservice.CreateInput{
		PluginID: sourceID,
		Content: pluginservice.ContentInput{
			PluginType:   contract.TypeExpert,
			ManifestJSON: []byte(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"CAS Expert","plugin_type":"expert","name":"CAS Expert","description":"one"}`),
			PluginJSON:   []byte("null"),
		},
	}); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 3)
	go func() {
		<-start
		_, err := service.Update(context.Background(), scope, actor, sourceID, pluginservice.UpdateInput{
			ExpectedLockVersion: 1,
			Content: pluginservice.ContentInput{
				PluginType:   contract.TypeExpert,
				ManifestJSON: []byte(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"CAS Expert","plugin_type":"expert","name":"CAS Expert","description":"two"}`),
				PluginJSON:   []byte("null"),
			},
		})
		results <- err
	}()
	go func() {
		<-start
		_, err := service.SetStatus(context.Background(), scope, actor, sourceID, pluginservice.SetStatusInput{
			ExpectedLockVersion: 1, Status: contract.StatusArchived,
		})
		results <- err
	}()
	go func() {
		<-start
		_, err := service.ReplaceRelations(context.Background(), scope, actor, sourceID, pluginservice.ReplaceRelationsInput{
			ExpectedLockVersion: 1,
			Relations: []pluginservice.RelationInput{{
				RelationType: contract.RelationExpertSkill, TargetPluginID: targetID,
			}},
		})
		results <- err
	}()
	close(start)
	succeeded, conflicted := 0, 0
	for range 3 {
		switch err := <-results; {
		case err == nil:
			succeeded++
		case errors.Is(err, pluginstore.ErrConflict):
			conflicted++
		default:
			t.Fatalf("mixed concurrent write error = %v", err)
		}
	}
	if succeeded != 1 || conflicted != 2 {
		t.Fatalf("mixed concurrent writes: success=%d conflict=%d", succeeded, conflicted)
	}
}

func TestVersionOverflowFailsClosed(t *testing.T) {
	db := integrationDatabase(t, "OCTO_PLUGIN_LIB_MYSQL_DSN")
	if err := Install(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	store, _ := New(db)
	service, _ := pluginservice.New(store)
	scope := pluginservice.Scope{ID: fmt.Sprintf("overflow-%d", time.Now().UnixNano())}
	actor := pluginservice.Actor{ID: "actor-test"}
	const revisionID = "22000000-0000-4000-8000-000000000001"
	created, err := service.Create(context.Background(), scope, actor, pluginservice.CreateInput{
		PluginID: revisionID, Content: rawSkill("Revision Overflow", "one"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_revision
(scope_id, plugin_id, revision_no, manifest_json, plugin_json, plugin_hash, created_by, created_at)
VALUES (?, ?, ?, CAST(? AS JSON), CAST(? AS JSON), ?, ?, ?)`, scope.ID, revisionID, uint64(math.MaxUint32),
		created.Revision.ManifestJSON, created.Revision.PluginJSON, created.Revision.PluginHash,
		actor.ID, created.Revision.CreatedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE plugin SET current_revision_no = ? WHERE scope_id = ? AND id = ?`,
		uint64(math.MaxUint32), scope.ID, revisionID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Update(context.Background(), scope, actor, revisionID, pluginservice.UpdateInput{
		ExpectedLockVersion: 1, Content: rawSkill("Revision Overflow", "two"),
	}); !errors.Is(err, pluginstore.ErrConflict) {
		t.Fatalf("Revision overflow error = %v", err)
	}

	const lockID = "22000000-0000-4000-8000-000000000002"
	if _, err := service.Create(context.Background(), scope, actor, pluginservice.CreateInput{
		PluginID: lockID, Content: rawSkill("Lock Overflow", "one"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE plugin SET lock_version = ? WHERE scope_id = ? AND id = ?`,
		uint64(math.MaxUint32), scope.ID, lockID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetStatus(context.Background(), scope, actor, lockID, pluginservice.SetStatusInput{
		ExpectedLockVersion: math.MaxUint32, Status: contract.StatusArchived,
	}); !errors.Is(err, pluginstore.ErrConflict) {
		t.Fatalf("lock overflow error = %v", err)
	}
}

func TestDatabaseChecksAreEnforced(t *testing.T) {
	db := integrationDatabase(t, "OCTO_PLUGIN_LIB_MYSQL_DSN")
	if err := Install(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	store, _ := New(db)
	service, _ := pluginservice.New(store)
	scope := pluginservice.Scope{ID: fmt.Sprintf("checks-%d", time.Now().UnixNano())}
	const pluginID = "30000000-0000-4000-8000-000000000001"
	created, err := service.Create(context.Background(), scope, pluginservice.Actor{ID: "actor-test"}, pluginservice.CreateInput{
		PluginID: pluginID, Content: rawSkill("Check Skill", "checks"),
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, query := range map[string]string{
		"plugin_hash": "UPDATE plugin_revision SET plugin_hash = 'bad' WHERE scope_id = ? AND plugin_id = ? AND revision_no = ?",
		"status":      "UPDATE plugin SET status = 'DISABLED' WHERE scope_id = ? AND id = ? AND current_revision_no = ?",
	} {
		_, err := db.Exec(query, scope.ID, pluginID, created.Revision.RevisionNo)
		var mysqlError *driver.MySQLError
		if !errors.As(err, &mysqlError) || mysqlError.Number != 3819 {
			t.Fatalf("%s CHECK error = %v", name, err)
		}
	}
}

func TestVerifySchemaRejectsDrift(t *testing.T) {
	db := integrationDatabase(t, "OCTO_PLUGIN_LIB_MYSQL_DRIFT_DSN")
	var driftExists bool
	if err := db.QueryRow(`SELECT EXISTS(
SELECT 1 FROM information_schema.columns
WHERE table_schema = DATABASE() AND table_name = 'plugin' AND column_name = 'host_only_drift')`).Scan(&driftExists); err != nil {
		t.Fatal(err)
	}
	if driftExists {
		if _, err := db.Exec("ALTER TABLE plugin DROP COLUMN host_only_drift"); err != nil {
			t.Fatal(err)
		}
	}
	if err := Install(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("ALTER TABLE plugin ADD COLUMN host_only_drift INT NULL"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec("ALTER TABLE plugin DROP COLUMN host_only_drift") })
	if err := VerifySchema(context.Background(), db); err == nil {
		t.Fatal("VerifySchema accepted a modified owned table")
	}
}

func integrationDatabase(t *testing.T, variable string) *sql.DB {
	t.Helper()
	dsn := os.Getenv(variable)
	if dsn == "" {
		t.Skip(variable + " is not set")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	return db
}

func rawSkill(name, description string) pluginservice.ContentInput {
	return pluginservice.ContentInput{
		PluginType: contract.TypeSkill,
		ManifestJSON: []byte(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"` + name +
			`","plugin_type":"skill","name":"` + name + `","description":"` + description + `"}`),
		PluginJSON: []byte(`{"$schema":"cowork-plugin-package-2.0.json","attachments":[{"path":"SKILL.md","content_type":"raw","mime_type":"text/markdown","raw_content":"# Skill"}]}`),
	}
}
