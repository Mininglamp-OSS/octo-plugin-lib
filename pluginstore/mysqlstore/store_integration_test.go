package mysqlstore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
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

func TestInstallReleasesAdvisoryLock(t *testing.T) {
	db := integrationDatabase(t, "OCTO_PLUGIN_LIB_MYSQL_DSN")
	if err := Install(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	probe, err := sql.Open("mysql", os.Getenv("OCTO_PLUGIN_LIB_MYSQL_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close() //nolint:errcheck
	probe.SetMaxOpenConns(1)
	var lockName string
	if err := probe.QueryRow("SELECT CONCAT('opl:', LEFT(SHA2(DATABASE(), 256), 60))").Scan(&lockName); err != nil {
		t.Fatal(err)
	}
	var owner sql.NullInt64
	if err := probe.QueryRow("SELECT IS_USED_LOCK(?)", lockName).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner.Valid {
		t.Fatalf("Install leaked advisory lock %q to connection %d", lockName, owner.Int64)
	}
}

func TestInstallRejectsIncompleteOwnedTables(t *testing.T) {
	db := integrationDatabase(t, "OCTO_PLUGIN_LIB_MYSQL_DRIFT_DSN")
	db.SetMaxOpenConns(1)
	resetOwnedTables(t, db)
	t.Cleanup(func() { resetOwnedTables(t, db) })

	if _, err := db.Exec("CREATE TABLE plugin (id INT PRIMARY KEY) ENGINE=InnoDB"); err != nil {
		t.Fatal(err)
	}
	if err := Install(context.Background(), db); err == nil || !strings.Contains(err.Error(), "found 1 of 3 owned tables") {
		t.Fatalf("Install incomplete-schema error = %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*)
FROM information_schema.tables
WHERE table_schema = DATABASE()
  AND table_name IN ('plugin', 'plugin_revision', 'plugin_relation')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("Install changed an incomplete schema: found %d owned tables", count)
	}
}

func TestConcurrentInstallIsSerialized(t *testing.T) {
	db := integrationDatabase(t, "OCTO_PLUGIN_LIB_MYSQL_DRIFT_DSN")
	db.SetMaxOpenConns(2)
	resetOwnedTables(t, db)

	start := make(chan struct{})
	errors := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			errors <- Install(context.Background(), db)
		}()
	}
	close(start)
	for range 2 {
		if err := <-errors; err != nil {
			t.Fatalf("concurrent Install: %v", err)
		}
	}
	if err := VerifySchema(context.Background(), db); err != nil {
		t.Fatalf("schema after concurrent Install: %v", err)
	}
}

func TestCanonicalJSONTextRoundTrip(t *testing.T) {
	db := integrationDatabase(t, "OCTO_PLUGIN_LIB_MYSQL_DSN")
	if err := Install(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	store, _ := New(db)
	service, _ := pluginservice.New(store)
	scope := pluginservice.Scope{ID: fmt.Sprintf("json-roundtrip-%d", time.Now().UnixNano())}
	actor := pluginservice.Actor{ID: "actor-test"}
	const pluginID = "94000000-0000-4000-8000-000000000001"
	input := rawSkill("JSON Roundtrip", "canonical")
	input.ManifestJSON = []byte(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"JSON Roundtrip","plugin_type":"skill","name":"JSON Roundtrip","description":"canonical","exact_number":1e10000}`)
	want, err := contract.NormalizeRevisionContent(contract.RevisionContent{
		PluginType: input.PluginType, ManifestJSON: input.ManifestJSON, PluginJSON: input.PluginJSON,
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), scope, actor, pluginservice.CreateInput{
		PluginID: pluginID, Content: input,
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err := service.Get(context.Background(), scope, pluginID)
	if err != nil {
		t.Fatal(err)
	}
	historical, err := service.GetRevision(context.Background(), scope, pluginID, 1)
	if err != nil {
		t.Fatal(err)
	}
	page, err := service.List(context.Background(), scope, pluginservice.ListFilter{Query: "JSON Roundtrip"})
	if err != nil {
		t.Fatal(err)
	}
	for name, revision := range map[string]pluginstore.Revision{
		"Create": created.Revision, "Get": current.Revision, "GetRevision": historical,
	} {
		if !bytes.Equal(revision.ManifestJSON, want.ManifestJSON) ||
			!bytes.Equal(revision.PluginJSON, want.PluginJSON) || revision.PluginHash != want.PluginHash {
			t.Errorf("%s returned non-canonical content: %#v", name, revision)
		}
	}
	if len(page.Items) != 1 || !bytes.Equal(page.Items[0].ManifestJSON, want.ManifestJSON) {
		t.Fatalf("List returned non-canonical Manifest: %#v", page)
	}
	var stored []byte
	if err := db.QueryRow(`SELECT manifest_json FROM plugin_revision
WHERE scope_id = ? AND plugin_id = ? AND revision_no = 1`, scope.ID, pluginID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, want.ManifestJSON) {
		t.Fatalf("database changed Canonical JSON: got %s, want %s", stored, want.ManifestJSON)
	}
}

func TestStoredJSONDriftFailsClosed(t *testing.T) {
	db := integrationDatabase(t, "OCTO_PLUGIN_LIB_MYSQL_DSN")
	if err := Install(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	store, _ := New(db)
	service, _ := pluginservice.New(store)
	scope := pluginservice.Scope{ID: fmt.Sprintf("json-drift-%d", time.Now().UnixNano())}
	const pluginID = "95000000-0000-4000-8000-000000000001"
	created, err := service.Create(context.Background(), scope, pluginservice.Actor{ID: "actor-test"}, pluginservice.CreateInput{
		PluginID: pluginID, Content: rawSkill("JSON Drift", "original"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE plugin_revision SET manifest_json = CONCAT(' ', manifest_json)
WHERE scope_id = ? AND plugin_id = ? AND revision_no = 1`, scope.ID, pluginID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(context.Background(), scope, pluginID); !errors.Is(err, pluginstore.ErrIntegrity) {
		t.Fatalf("Get non-canonical JSON error = %v", err)
	}
	if _, err := service.GetRevision(context.Background(), scope, pluginID, 1); !errors.Is(err, pluginstore.ErrIntegrity) {
		t.Fatalf("GetRevision non-canonical JSON error = %v", err)
	}
	if _, err := service.List(context.Background(), scope, pluginservice.ListFilter{}); !errors.Is(err, pluginstore.ErrIntegrity) {
		t.Fatalf("List non-canonical JSON error = %v", err)
	}
	if _, err := db.Exec(`UPDATE plugin_revision SET manifest_json = ?
WHERE scope_id = ? AND plugin_id = ? AND revision_no = 1`, created.Revision.ManifestJSON, scope.ID, pluginID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE plugin_revision
SET manifest_json = JSON_SET(manifest_json, '$.description', 'changed outside the Store')
WHERE scope_id = ? AND plugin_id = ? AND revision_no = 1`, scope.ID, pluginID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(context.Background(), scope, pluginID); !errors.Is(err, pluginstore.ErrIntegrity) {
		t.Fatalf("Get drift error = %v", err)
	}
	if _, err := service.GetRevision(context.Background(), scope, pluginID, 1); !errors.Is(err, pluginstore.ErrIntegrity) {
		t.Fatalf("GetRevision drift error = %v", err)
	}
	if _, err := db.Exec(`UPDATE plugin_revision SET manifest_json = '{broken'
WHERE scope_id = ? AND plugin_id = ? AND revision_no = 1`, scope.ID, pluginID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(context.Background(), scope, pluginID); !errors.Is(err, pluginstore.ErrIntegrity) {
		t.Fatalf("Get invalid JSON error = %v", err)
	}
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

func TestFailedBoundCallRollsBackOnlyItsSavepoint(t *testing.T) {
	db := integrationDatabase(t, "OCTO_PLUGIN_LIB_MYSQL_DSN")
	if err := Install(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	store, _ := New(db)
	service, _ := pluginservice.New(store)
	scope := pluginservice.Scope{ID: fmt.Sprintf("savepoint-%d", time.Now().UnixNano())}
	actor := pluginservice.Actor{ID: "actor-test"}
	const rootID = "a1000000-0000-4000-8000-000000000001"
	const existingID = "f1000000-0000-4000-8000-000000000001"
	if _, err := service.Create(context.Background(), scope, actor, pluginservice.CreateInput{
		PluginID: existingID, Content: nullPlugin(contract.TypeExpert, "Existing Expert"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS plugin_host_tx_probe (
request_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY
) ENGINE=InnoDB`); err != nil {
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
	beforeID := fmt.Sprintf("before-%d", time.Now().UnixNano())
	afterID := fmt.Sprintf("after-%d", time.Now().UnixNano())
	if _, err := tx.Exec("INSERT INTO plugin_host_tx_probe (request_id) VALUES (?)", beforeID); err != nil {
		t.Fatal(err)
	}
	_, err = bound.CreateGraph(context.Background(), scope, actor, pluginservice.GraphCreateInput{
		RootPluginID: rootID,
		Nodes: []pluginservice.GraphNodeInput{
			{PluginID: rootID, Content: nullPlugin(contract.TypeExpertTeam, "Savepoint Team"), Relations: []pluginservice.RelationInput{{RelationType: contract.RelationExpertTeamExpert, TargetPluginID: existingID}}},
			{PluginID: existingID, Content: nullPlugin(contract.TypeExpert, "Duplicate Expert")},
		},
	})
	if !errors.Is(err, pluginstore.ErrAlreadyExists) || errors.Is(err, pluginstore.ErrTransactionAborted) {
		t.Fatalf("failing bound CreateGraph error = %v", err)
	}
	if _, err := tx.Exec("INSERT INTO plugin_host_tx_probe (request_id) VALUES (?)", afterID); err != nil {
		t.Fatalf("host transaction unusable after savepoint rollback: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var hostRows, orphanRows int
	if err := db.QueryRow("SELECT COUNT(*) FROM plugin_host_tx_probe WHERE request_id IN (?, ?)", beforeID, afterID).Scan(&hostRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM plugin WHERE scope_id = ? AND id = ?", scope.ID, rootID).Scan(&orphanRows); err != nil {
		t.Fatal(err)
	}
	if hostRows != 2 || orphanRows != 0 {
		t.Fatalf("savepoint result: host rows=%d orphan rows=%d", hostRows, orphanRows)
	}
}

func TestListExcludesIncompletePluginRows(t *testing.T) {
	db := integrationDatabase(t, "OCTO_PLUGIN_LIB_MYSQL_DSN")
	if err := Install(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	store, _ := New(db)
	service, _ := pluginservice.New(store)
	scope := pluginservice.Scope{ID: fmt.Sprintf("incomplete-%d", time.Now().UnixNano())}
	const pluginID = "a2000000-0000-4000-8000-000000000001"
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := db.Exec(`INSERT INTO plugin
(scope_id, id, name, type, status, current_revision_no, lock_version, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, NULL, 1, ?, ?)`, scope.ID, pluginID, "Incomplete Skill",
		contract.TypeSkill, contract.StatusActive, now, now); err != nil {
		t.Fatal(err)
	}
	page, err := service.List(context.Background(), scope, pluginservice.ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("List exposed incomplete Plugin: total=%d items=%d", page.Total, len(page.Items))
	}
}

func TestBoundWriteUsesCurrentReadAfterEarlierSnapshot(t *testing.T) {
	db := integrationDatabase(t, "OCTO_PLUGIN_LIB_MYSQL_DSN")
	if err := Install(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	store, _ := New(db)
	service, _ := pluginservice.New(store)
	scope := pluginservice.Scope{ID: fmt.Sprintf("current-read-%d", time.Now().UnixNano())}
	actor := pluginservice.Actor{ID: "actor-test"}
	const pluginID = "b1000000-0000-4000-8000-000000000001"
	first := rawSkill("Snapshot Skill", "first")
	second := rawSkill("Snapshot Skill", "second")
	if _, err := service.Create(context.Background(), scope, actor, pluginservice.CreateInput{PluginID: pluginID, Content: first}); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck
	bound, _ := service.WithTx(tx)
	if _, err := bound.Get(context.Background(), scope, pluginID); err != nil {
		t.Fatal(err)
	}
	outside, err := service.Update(context.Background(), scope, actor, pluginID, pluginservice.UpdateInput{
		ExpectedLockVersion: 1, Content: second,
	})
	if err != nil {
		t.Fatal(err)
	}
	inside, err := bound.Update(context.Background(), scope, actor, pluginID, pluginservice.UpdateInput{
		ExpectedLockVersion: outside.Plugin.LockVersion, Content: first,
	})
	if err != nil {
		t.Fatal(err)
	}
	if inside.Revision.RevisionNo != 3 || inside.Plugin.LockVersion != 3 {
		t.Fatalf("bound current read returned stale snapshot: %#v", inside)
	}
}

func TestInstallRejectsInvalidTimeConfiguration(t *testing.T) {
	dsn := os.Getenv("OCTO_PLUGIN_LIB_MYSQL_DSN")
	if dsn == "" {
		if os.Getenv("OCTO_PLUGIN_LIB_REQUIRE_MYSQL") == "1" {
			t.Fatal("OCTO_PLUGIN_LIB_MYSQL_DSN is required")
		}
		t.Skip("OCTO_PLUGIN_LIB_MYSQL_DSN is not set")
	}
	base, err := driver.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	withoutParseTime := *base
	withoutParseTime.ParseTime = false
	wrongLocation := *base
	wrongLocation.ParseTime = true
	wrongLocation.Loc = newYork
	truncated := base.FormatDSN()
	separator := "?"
	if strings.Contains(truncated, "?") {
		separator = "&"
	}
	truncated += separator + "timeTruncate=1s"
	for name, invalidDSN := range map[string]string{
		"parseTime disabled": withoutParseTime.FormatDSN(),
		"non-UTC location":   wrongLocation.FormatDSN(),
		"time truncation":    truncated,
	} {
		t.Run(name, func(t *testing.T) {
			database, err := sql.Open("mysql", invalidDSN)
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close() //nolint:errcheck
			err = Install(context.Background(), database)
			if err == nil || !strings.Contains(err.Error(), "parseTime=true&loc=UTC") {
				t.Fatalf("Install error = %v", err)
			}
		})
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
		_, err := service.SetStatus(context.Background(), scope, sourceID, pluginservice.SetStatusInput{
			ExpectedLockVersion: 1, Status: contract.StatusArchived,
		})
		results <- err
	}()
	go func() {
		<-start
		_, err := service.ReplaceRelations(context.Background(), scope, sourceID, pluginservice.ReplaceRelationsInput{
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

func TestGetReturnsOneCurrentSnapshot(t *testing.T) {
	db := integrationDatabase(t, "OCTO_PLUGIN_LIB_MYSQL_DSN")
	if err := Install(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	store, _ := New(db)
	service, _ := pluginservice.New(store)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	scope := pluginservice.Scope{ID: fmt.Sprintf("snapshot-%d", time.Now().UnixNano())}
	actor := pluginservice.Actor{ID: "actor-test"}
	const (
		sourceID = "91000000-0000-4000-8000-000000000001"
		targetA  = "91000000-0000-4000-8000-000000000002"
		targetB  = "91000000-0000-4000-8000-000000000003"
	)
	for _, target := range []struct {
		id   string
		name string
	}{{targetA, "Target A"}, {targetB, "Target B"}} {
		if _, err := service.Create(ctx, scope, actor, pluginservice.CreateInput{
			PluginID: target.id, Content: rawSkill(target.name, "snapshot target"),
		}); err != nil {
			t.Fatal(err)
		}
	}
	current, err := service.Create(ctx, scope, actor, pluginservice.CreateInput{
		PluginID: sourceID,
		Content:  nullPlugin(contract.TypeExpert, "Snapshot Expert"),
		Relations: []pluginservice.RelationInput{{
			RelationType: contract.RelationExpertSkill, TargetPluginID: targetA,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	verifyConcurrentReads(t, 300, func() error {
		snapshot, err := service.Get(ctx, scope, sourceID)
		if err != nil {
			return err
		}
		want := targetA
		if snapshot.Plugin.LockVersion%2 == 0 {
			want = targetB
		}
		if len(snapshot.Relations) != 1 || snapshot.Relations[0].TargetPluginID != want {
			return fmt.Errorf("lock_version=%d relations=%v, want target %s",
				snapshot.Plugin.LockVersion, snapshot.Relations, want)
		}
		return nil
	}, func(index int) error {
		target := targetB
		if index%2 == 1 {
			target = targetA
		}
		next, err := service.ReplaceRelations(ctx, scope, sourceID, pluginservice.ReplaceRelationsInput{
			ExpectedLockVersion: current.Plugin.LockVersion,
			Relations: []pluginservice.RelationInput{{
				RelationType: contract.RelationExpertSkill, TargetPluginID: target,
			}},
		})
		if err == nil {
			current = next
		}
		return err
	})
}

func TestListReturnsOneCurrentSnapshot(t *testing.T) {
	db := integrationDatabase(t, "OCTO_PLUGIN_LIB_MYSQL_DSN")
	if err := Install(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	store, _ := New(db)
	service, _ := pluginservice.New(store)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	scope := pluginservice.Scope{ID: fmt.Sprintf("list-snapshot-%d", time.Now().UnixNano())}
	actor := pluginservice.Actor{ID: "actor-test"}
	const pluginID = "92000000-0000-4000-8000-000000000001"
	current, err := service.Create(ctx, scope, actor, pluginservice.CreateInput{
		PluginID: pluginID, Content: rawSkill("List Snapshot", "snapshot"),
	})
	if err != nil {
		t.Fatal(err)
	}

	active := contract.StatusActive
	verifyConcurrentReads(t, 200, func() error {
		page, err := service.List(ctx, scope, pluginservice.ListFilter{Status: &active})
		if err != nil {
			return err
		}
		if page.Total != int64(len(page.Items)) ||
			len(page.Items) == 1 && page.Items[0].Status != contract.StatusActive {
			return fmt.Errorf("active List total=%d items=%v", page.Total, page.Items)
		}
		return nil
	}, func(index int) error {
		status := contract.StatusArchived
		if index%2 == 1 {
			status = contract.StatusActive
		}
		next, err := service.SetStatus(ctx, scope, pluginID, pluginservice.SetStatusInput{
			ExpectedLockVersion: current.Plugin.LockVersion, Status: status,
		})
		if err == nil {
			current = next
		}
		return err
	})
}

func TestListRevisionsReturnsOneCurrentSnapshot(t *testing.T) {
	db := integrationDatabase(t, "OCTO_PLUGIN_LIB_MYSQL_DSN")
	if err := Install(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	store, _ := New(db)
	service, _ := pluginservice.New(store)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	scope := pluginservice.Scope{ID: fmt.Sprintf("revision-snapshot-%d", time.Now().UnixNano())}
	actor := pluginservice.Actor{ID: "actor-test"}
	const pluginID = "93000000-0000-4000-8000-000000000001"
	current, err := service.Create(ctx, scope, actor, pluginservice.CreateInput{
		PluginID: pluginID, Content: rawSkill("Revision Snapshot", "revision-0"),
	})
	if err != nil {
		t.Fatal(err)
	}

	verifyConcurrentReads(t, 50, func() error {
		page, err := service.ListRevisions(ctx, scope, pluginID, pluginservice.PageRequest{PageSize: 100})
		if err != nil {
			return err
		}
		if page.Total != int64(len(page.Items)) || len(page.Items) == 0 ||
			page.Items[0].RevisionNo != uint32(page.Total) {
			return fmt.Errorf("Revision page total=%d items=%v", page.Total, page.Items)
		}
		return nil
	}, func(index int) error {
		next, err := service.Update(ctx, scope, actor, pluginID, pluginservice.UpdateInput{
			ExpectedLockVersion: current.Plugin.LockVersion,
			Content:             rawSkill("Revision Snapshot", fmt.Sprintf("revision-%d", index+1)),
		})
		if err == nil {
			current = next
		}
		return err
	})
}

func verifyConcurrentReads(t *testing.T, writes int, read func() error, write func(int) error) {
	t.Helper()
	const readerCount = 8
	done := make(chan struct{})
	ready := make(chan struct{}, readerCount)
	errorsFound := make(chan error, readerCount+1)
	var readers sync.WaitGroup
	for range readerCount {
		readers.Add(1)
		go func() {
			defer readers.Done()
			first := true
			for {
				select {
				case <-done:
					return
				default:
				}
				err := read()
				if first {
					ready <- struct{}{}
					first = false
				}
				if err != nil {
					errorsFound <- err
					return
				}
			}
		}()
	}
	for range readerCount {
		<-ready
	}
	for index := 0; index < writes; index++ {
		if err := write(index); err != nil {
			errorsFound <- err
			break
		}
	}
	close(done)
	readers.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
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
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, scope.ID, revisionID, uint64(math.MaxUint32),
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
	if _, err := service.SetStatus(context.Background(), scope, lockID, pluginservice.SetStatusInput{
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

	assertCheckFailure := func(name, query string, arguments ...any) {
		t.Helper()
		_, err := db.Exec(query, arguments...)
		var mysqlError *driver.MySQLError
		if !errors.As(err, &mysqlError) || mysqlError.Number != 3819 {
			t.Fatalf("%s CHECK error = %v", name, err)
		}
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	const insertPlugin = `INSERT INTO plugin
(scope_id, id, name, type, status, current_revision_no, lock_version, created_at, updated_at)
VALUES (?, ?, 'Invalid', 'skill', 'ACTIVE', NULL, 1, ?, ?)`
	assertCheckFailure("scope_id", insertPlugin,
		"bad\nscope", "30000000-0000-4000-8000-000000000002", now, now)
	assertCheckFailure("plugin_id", insertPlugin,
		scope.ID, "30000000-0000-4000-8000-00000000000A", now, now)
	assertCheckFailure("created_by", `INSERT INTO plugin_revision
(scope_id, plugin_id, revision_no, manifest_json, plugin_json, plugin_hash, created_by, created_at)
VALUES (?, ?, 2, ?, ?, ?, ?, ?)`, scope.ID, pluginID,
		created.Revision.ManifestJSON, created.Revision.PluginJSON, created.Revision.PluginHash, "bad\nactor", now)
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
		if os.Getenv("OCTO_PLUGIN_LIB_REQUIRE_MYSQL") == "1" {
			t.Fatal(variable + " is required")
		}
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

func resetOwnedTables(t *testing.T, db *sql.DB) {
	t.Helper()
	connection, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close() //nolint:errcheck
	if _, err := connection.ExecContext(context.Background(), "SET FOREIGN_KEY_CHECKS = 0"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := connection.ExecContext(context.Background(), "SET FOREIGN_KEY_CHECKS = 1"); err != nil {
			t.Errorf("restore FOREIGN_KEY_CHECKS: %v", err)
		}
	}()
	if _, err := connection.ExecContext(context.Background(), "DROP TABLE IF EXISTS plugin_relation, plugin_revision, plugin"); err != nil {
		t.Fatal(err)
	}
}

func nullPlugin(pluginType contract.Type, name string) pluginservice.ContentInput {
	return pluginservice.ContentInput{
		PluginType: pluginType,
		ManifestJSON: []byte(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"` + name +
			`","plugin_type":"` + string(pluginType) + `","name":"` + name + `","description":""}`),
		PluginJSON: []byte("null"),
	}
}

func rawSkill(name, description string) pluginservice.ContentInput {
	return pluginservice.ContentInput{
		PluginType: contract.TypeSkill,
		ManifestJSON: []byte(`{"$schema":"cowork-plugin-manifest-2.0.json","plugin_name":"` + name +
			`","plugin_type":"skill","name":"` + name + `","description":"` + description + `"}`),
		PluginJSON: []byte(`{"$schema":"cowork-plugin-package-2.0.json","attachments":[{"path":"SKILL.md","content_type":"raw","mime_type":"text/markdown","raw_content":"# Skill"}]}`),
	}
}
