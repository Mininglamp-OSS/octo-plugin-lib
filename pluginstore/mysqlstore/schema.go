// Package mysqlstore implements the shared Plugin Store for MySQL 8.
package mysqlstore

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	driver "github.com/go-sql-driver/mysql"
)

//go:embed schema.sql
var schemaSQL string

func SchemaSQL() string { return schemaSQL }

func Install(ctx context.Context, db *sql.DB) (err error) {
	if db == nil {
		return errors.New("mysqlstore: database is required")
	}
	connection, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("mysqlstore: reserve install connection: %w", err)
	}
	defer connection.Close() //nolint:errcheck
	if err := verifyTimeConfiguration(ctx, connection); err != nil {
		return err
	}

	var lockName string
	if err := connection.QueryRowContext(ctx, "SELECT CONCAT('opl:', LEFT(SHA2(DATABASE(), 256), 60))").Scan(&lockName); err != nil {
		return fmt.Errorf("mysqlstore: resolve install lock: %w", err)
	}
	var acquired int
	if err := connection.QueryRowContext(ctx, "SELECT GET_LOCK(?, 30)", lockName).Scan(&acquired); err != nil || acquired != 1 {
		if err == nil {
			err = errors.New("lock was not acquired")
		}
		return fmt.Errorf("mysqlstore: acquire install lock: %w", err)
	}
	defer func() {
		var released int
		releaseErr := connection.QueryRowContext(context.Background(), "SELECT RELEASE_LOCK(?)", lockName).Scan(&released)
		if err == nil && releaseErr != nil {
			err = fmt.Errorf("mysqlstore: release install lock: %w", releaseErr)
		}
	}()

	for _, statement := range strings.Split(schemaSQL, "-- statement") {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		if _, executeErr := connection.ExecContext(ctx, statement); executeErr != nil && !duplicateConstraint(executeErr) {
			return fmt.Errorf("mysqlstore: install schema: %w", executeErr)
		}
	}
	return VerifySchema(ctx, connection)
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// VerifySchema rejects drift in every owned table, column, constraint and
// index. Hosts may add separate extension tables, but do not alter these three.
func VerifySchema(ctx context.Context, db queryer) error {
	if db == nil {
		return errors.New("mysqlstore: database is required")
	}
	actual, err := schemaFingerprint(ctx, db)
	if err != nil {
		return err
	}
	if actual != expectedSchemaFingerprint {
		return fmt.Errorf("mysqlstore: Plugin schema differs from the required baseline: %s",
			fingerprintDifference(expectedSchemaFingerprint, actual))
	}
	return nil
}

func verifyTimeConfiguration(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) error {
	const layout = "2006-01-02 15:04:05.999999"
	want := time.Date(2001, 2, 3, 4, 5, 6, 123456000, time.UTC)
	var encoded string
	if err := db.QueryRowContext(ctx, "SELECT CAST(? AS CHAR)", want).Scan(&encoded); err != nil {
		return fmt.Errorf("mysqlstore: verify DSN time encoding: %w", err)
	}
	if encoded != want.Format(layout) {
		return errors.New("mysqlstore: DSN must include parseTime=true&loc=UTC without time truncation")
	}
	var decoded time.Time
	if err := db.QueryRowContext(ctx, "SELECT CAST(? AS DATETIME(6))", encoded).Scan(&decoded); err != nil {
		return fmt.Errorf("mysqlstore: DSN must include parseTime=true&loc=UTC: %w", err)
	}
	if decoded.Location() != time.UTC || decoded.Format(layout) != encoded {
		return errors.New("mysqlstore: DSN must include parseTime=true&loc=UTC without time truncation")
	}
	return nil
}

func fingerprintDifference(expected, actual string) string {
	want := lineSet(expected)
	got := lineSet(actual)
	missing := setDifference(want, got)
	unexpected := setDifference(got, want)
	return fmt.Sprintf("missing=%q unexpected=%q", missing, unexpected)
}

func lineSet(value string) map[string]struct{} {
	result := make(map[string]struct{})
	for _, line := range strings.Split(value, "\n") {
		if line != "" {
			result[line] = struct{}{}
		}
	}
	return result
}

func setDifference(left, right map[string]struct{}) []string {
	result := make([]string, 0)
	for line := range left {
		if _, exists := right[line]; !exists {
			result = append(result, line)
		}
	}
	sort.Strings(result)
	return result
}

func duplicateConstraint(err error) bool {
	var mysqlError *driver.MySQLError
	return errors.As(err, &mysqlError) && mysqlError.Number == 1826
}

// Filled from information_schema and guarded by schema tests. Keeping one
// stable fingerprint is smaller and less error-prone than a parallel DDL AST.
const expectedSchemaFingerprint = `C|plugin_relation|001|scope_id|varchar(40)|NO|<NULL>||ascii|ascii_bin
C|plugin_relation|002|source_plugin_id|char(36)|NO|<NULL>||ascii|ascii_bin
C|plugin_relation|003|relation_type|varchar(32)|NO|<NULL>||ascii|ascii_bin
C|plugin_relation|004|target_plugin_id|char(36)|NO|<NULL>||ascii|ascii_bin
C|plugin_revision|001|scope_id|varchar(40)|NO|<NULL>||ascii|ascii_bin
C|plugin_revision|002|plugin_id|char(36)|NO|<NULL>||ascii|ascii_bin
C|plugin_revision|003|revision_no|int unsigned|NO|<NULL>|||
C|plugin_revision|004|manifest_json|longtext|NO|<NULL>||utf8mb4|utf8mb4_bin
C|plugin_revision|005|plugin_json|longtext|NO|<NULL>||utf8mb4|utf8mb4_bin
C|plugin_revision|006|plugin_hash|char(71)|NO|<NULL>||ascii|ascii_bin
C|plugin_revision|007|created_by|varchar(191)|NO|<NULL>||utf8mb4|utf8mb4_0900_ai_ci
C|plugin_revision|008|created_at|datetime(6)|NO|<NULL>|||
C|plugin|001|scope_id|varchar(40)|NO|<NULL>||ascii|ascii_bin
C|plugin|002|id|char(36)|NO|<NULL>||ascii|ascii_bin
C|plugin|003|name|varchar(160)|NO|<NULL>||utf8mb4|utf8mb4_0900_ai_ci
C|plugin|004|description|longtext|NO|<NULL>||utf8mb4|utf8mb4_bin
C|plugin|005|type|varchar(16)|NO|<NULL>||ascii|ascii_bin
C|plugin|006|status|varchar(16)|NO|<NULL>||ascii|ascii_bin
C|plugin|007|current_revision_no|int unsigned|YES|<NULL>|||
C|plugin|008|lock_version|int unsigned|NO|<NULL>|||
C|plugin|009|created_at|datetime(6)|NO|<NULL>|||
C|plugin|010|updated_at|datetime(6)|NO|<NULL>|||
F|plugin_relation|fk_plugin_relation_source|001|scope_id|plugin|scope_id|NO ACTION|CASCADE
F|plugin_relation|fk_plugin_relation_source|002|source_plugin_id|plugin|id|NO ACTION|CASCADE
F|plugin_relation|fk_plugin_relation_target|001|scope_id|plugin|scope_id|NO ACTION|RESTRICT
F|plugin_relation|fk_plugin_relation_target|002|target_plugin_id|plugin|id|NO ACTION|RESTRICT
F|plugin_revision|fk_plugin_revision_plugin|001|scope_id|plugin|scope_id|NO ACTION|RESTRICT
F|plugin_revision|fk_plugin_revision_plugin|002|plugin_id|plugin|id|NO ACTION|RESTRICT
F|plugin|fk_plugin_current_revision|001|scope_id|plugin_revision|scope_id|NO ACTION|RESTRICT
F|plugin|fk_plugin_current_revision|002|id|plugin_revision|plugin_id|NO ACTION|RESTRICT
F|plugin|fk_plugin_current_revision|003|current_revision_no|plugin_revision|revision_no|NO ACTION|RESTRICT
H|plugin_relation|chk_plugin_relation_type|(relation_type in (_utf8mb4\'expert_team_expert\',_utf8mb4\'expert_skill\',_utf8mb4\'expert_connector\'))
H|plugin_revision|chk_plugin_revision_no|(revision_no > 0)
H|plugin_revision|chk_plugin_revision_plugin_hash|regexp_like(plugin_hash,_utf8mb4\'^sha256:[0-9a-f]{64}$\',_utf8mb4\'c\')
H|plugin|chk_plugin_lock|(lock_version > 0)
H|plugin|chk_plugin_status|(status in (_ascii\'ACTIVE\',_ascii\'ARCHIVED\'))
H|plugin|chk_plugin_time|(updated_at >= created_at)
H|plugin|chk_plugin_type|(type in (_ascii\'expert\',_ascii\'expert_team\',_ascii\'skill\',_ascii\'connector\'))
I|plugin_relation|PRIMARY|001|scope_id|0|A||BTREE|YES|
I|plugin_relation|PRIMARY|002|source_plugin_id|0|A||BTREE|YES|
I|plugin_relation|PRIMARY|003|relation_type|0|A||BTREE|YES|
I|plugin_relation|PRIMARY|004|target_plugin_id|0|A||BTREE|YES|
I|plugin_relation|idx_plugin_relation_target|001|scope_id|1|A||BTREE|YES|
I|plugin_relation|idx_plugin_relation_target|002|target_plugin_id|1|A||BTREE|YES|
I|plugin_revision|PRIMARY|001|scope_id|0|A||BTREE|YES|
I|plugin_revision|PRIMARY|002|plugin_id|0|A||BTREE|YES|
I|plugin_revision|PRIMARY|003|revision_no|0|A||BTREE|YES|
I|plugin|PRIMARY|001|scope_id|0|A||BTREE|YES|
I|plugin|PRIMARY|002|id|0|A||BTREE|YES|
I|plugin|fk_plugin_current_revision|001|scope_id|1|A||BTREE|YES|
I|plugin|fk_plugin_current_revision|002|id|1|A||BTREE|YES|
I|plugin|fk_plugin_current_revision|003|current_revision_no|1|A||BTREE|YES|
I|plugin|idx_plugin_scope_updated|001|scope_id|1|A||BTREE|YES|
I|plugin|idx_plugin_scope_updated|002|updated_at|1|D||BTREE|YES|
I|plugin|idx_plugin_scope_updated|003|id|1|D||BTREE|YES|
K|plugin_relation|PRIMARY|PRIMARY KEY|YES
K|plugin_relation|chk_plugin_relation_type|CHECK|YES
K|plugin_relation|fk_plugin_relation_source|FOREIGN KEY|YES
K|plugin_relation|fk_plugin_relation_target|FOREIGN KEY|YES
K|plugin_revision|PRIMARY|PRIMARY KEY|YES
K|plugin_revision|chk_plugin_revision_no|CHECK|YES
K|plugin_revision|chk_plugin_revision_plugin_hash|CHECK|YES
K|plugin_revision|fk_plugin_revision_plugin|FOREIGN KEY|YES
K|plugin|PRIMARY|PRIMARY KEY|YES
K|plugin|chk_plugin_lock|CHECK|YES
K|plugin|chk_plugin_status|CHECK|YES
K|plugin|chk_plugin_time|CHECK|YES
K|plugin|chk_plugin_type|CHECK|YES
K|plugin|fk_plugin_current_revision|FOREIGN KEY|YES
T|plugin_relation|InnoDB|utf8mb4_0900_ai_ci
T|plugin_revision|InnoDB|utf8mb4_0900_ai_ci
T|plugin|InnoDB|utf8mb4_0900_ai_ci`

func schemaFingerprint(ctx context.Context, db queryer) (string, error) {
	rows, err := db.QueryContext(ctx, schemaFingerprintQuery)
	if err != nil {
		return "", fmt.Errorf("mysqlstore: inspect schema: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return "", fmt.Errorf("mysqlstore: inspect schema row: %w", err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("mysqlstore: inspect schema rows: %w", err)
	}
	return strings.Join(lines, "\n"), nil
}

const schemaFingerprintQuery = `
SELECT line FROM (
  SELECT CONCAT('C|', table_name, '|', LPAD(ordinal_position, 3, '0'), '|', column_name, '|', column_type, '|', is_nullable, '|', COALESCE(column_default, '<NULL>'), '|', extra, '|', COALESCE(character_set_name, ''), '|', COALESCE(collation_name, '')) AS line
  FROM information_schema.columns
  WHERE table_schema = DATABASE() AND table_name IN ('plugin', 'plugin_revision', 'plugin_relation')
  UNION ALL
  SELECT CONCAT('K|', table_name, '|', constraint_name, '|', constraint_type, '|', enforced) AS line
  FROM information_schema.table_constraints
  WHERE table_schema = DATABASE() AND table_name IN ('plugin', 'plugin_revision', 'plugin_relation')
  UNION ALL
  SELECT CONCAT('H|', tc.table_name, '|', cc.constraint_name, '|', REPLACE(cc.check_clause, CHAR(96), '')) AS line
  FROM information_schema.table_constraints tc
  JOIN information_schema.check_constraints cc
    ON cc.constraint_schema = tc.constraint_schema AND cc.constraint_name = tc.constraint_name
  WHERE tc.table_schema = DATABASE()
    AND tc.table_name IN ('plugin', 'plugin_revision', 'plugin_relation')
    AND tc.constraint_type = 'CHECK'
  UNION ALL
  SELECT CONCAT('I|', table_name, '|', index_name, '|', LPAD(seq_in_index, 3, '0'), '|', column_name, '|', non_unique, '|', COALESCE(collation, ''), '|', COALESCE(CAST(sub_part AS CHAR), ''), '|', index_type, '|', is_visible, '|', COALESCE(expression, '')) AS line
  FROM information_schema.statistics
  WHERE table_schema = DATABASE() AND table_name IN ('plugin', 'plugin_revision', 'plugin_relation')
  UNION ALL
  SELECT CONCAT('F|', k.table_name, '|', k.constraint_name, '|', LPAD(k.ordinal_position, 3, '0'), '|', k.column_name, '|', k.referenced_table_name, '|', k.referenced_column_name, '|', r.update_rule, '|', r.delete_rule) AS line
  FROM information_schema.key_column_usage k
  JOIN information_schema.referential_constraints r
    ON r.constraint_schema = k.constraint_schema AND r.constraint_name = k.constraint_name
  WHERE k.table_schema = DATABASE() AND k.table_name IN ('plugin', 'plugin_revision', 'plugin_relation')
  UNION ALL
  SELECT CONCAT('T|', table_name, '|', engine, '|', table_collation) AS line
  FROM information_schema.tables
  WHERE table_schema = DATABASE() AND table_name IN ('plugin', 'plugin_revision', 'plugin_relation')
) fingerprint
ORDER BY BINARY line`
