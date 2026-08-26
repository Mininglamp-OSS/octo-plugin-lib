package mysqlstore

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-plugin-lib/pluginstore"
	driver "github.com/go-sql-driver/mysql"
)

func TestSchemaMatchesTheSharedThreeTableModel(t *testing.T) {
	ddl := SchemaSQL()
	for _, table := range []string{"plugin", "plugin_revision", "plugin_relation"} {
		if !strings.Contains(ddl, "CREATE TABLE IF NOT EXISTS "+table+" (") {
			t.Errorf("schema does not create %s", table)
		}
	}
	if count := strings.Count(ddl, "CREATE TABLE IF NOT EXISTS "); count != 3 {
		t.Fatalf("schema creates %d tables, want 3", count)
	}
	relation := regexp.MustCompile(`(?s)CREATE TABLE IF NOT EXISTS plugin_relation \((.*?)\) ENGINE`).FindStringSubmatch(ddl)
	if len(relation) != 2 {
		t.Fatal("plugin_relation DDL not found")
	}
	for _, removed := range []string{"relation_id", "revision_id", "status", "created_at", "updated_at"} {
		if strings.Contains(relation[1], removed) {
			t.Errorf("plugin_relation still contains %s", removed)
		}
	}
	if strings.Contains(ddl, "plugin_file") {
		t.Fatal("schema still creates removed plugin_file")
	}
	for _, required := range []string{
		"scope_id VARCHAR(40)", "status VARCHAR(16)", "current_revision_no INT UNSIGNED",
		"lock_version INT UNSIGNED", "chk_plugin_revision_plugin_hash", "chk_plugin_status",
		"description LONGTEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL",
		"manifest_json LONGTEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL",
		"plugin_json LONGTEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL",
		"PRIMARY KEY (scope_id, plugin_id, revision_no)",
		"PRIMARY KEY (scope_id, source_plugin_id, relation_type, target_plugin_id)",
		"KEY idx_plugin_scope_updated (scope_id, updated_at DESC, id DESC)",
		"FOREIGN KEY (scope_id, id, current_revision_no)",
	} {
		if !strings.Contains(ddl, required) {
			t.Errorf("schema is missing %q", required)
		}
	}
	if strings.Contains(ddl, "JSON_VALID") {
		t.Fatal("schema delegates the public JSON numeric domain to MySQL")
	}
}

func TestSchemaFingerprintIsPinned(t *testing.T) {
	if expectedSchemaFingerprint == "" ||
		!strings.Contains(expectedSchemaFingerprint, "H|plugin_revision|chk_plugin_revision_plugin_hash|") ||
		!strings.Contains(expectedSchemaFingerprint, "T|plugin|InnoDB|utf8mb4_0900_ai_ci") {
		t.Fatal("schema fingerprint is incomplete")
	}
}

func TestFingerprintDifferenceIsActionable(t *testing.T) {
	got := fingerprintDifference("column-a\ncolumn-b", "column-b\ncolumn-c")
	if got != `missing=["column-a"] unexpected=["column-c"]` {
		t.Fatalf("fingerprintDifference() = %s", got)
	}
}

func TestStorageErrorPreservesContextCancellation(t *testing.T) {
	err := storageError("query", context.Canceled)
	if !errors.Is(err, pluginstore.ErrStorage) || !errors.Is(err, context.Canceled) {
		t.Fatalf("storageError() = %v", err)
	}
}

func TestNilStoreGetRevisionFailsClosed(t *testing.T) {
	var store *Store
	if _, err := store.GetRevision(context.Background(), "scope", "plugin", 1); !errors.Is(err, pluginstore.ErrStorage) {
		t.Fatalf("GetRevision() error = %v, want ErrStorage", err)
	}
}

func TestRetryableMySQLErrorsAreConflicts(t *testing.T) {
	for _, number := range []uint16{1205, 1213} {
		err := mapWriteError("update", &driver.MySQLError{Number: number})
		if !errors.Is(err, pluginstore.ErrConflict) {
			t.Errorf("MySQL error %d mapped to %v", number, err)
		}
	}
}
