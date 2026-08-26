-- statement
CREATE TABLE IF NOT EXISTS plugin (
  scope_id VARCHAR(40) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  name VARCHAR(160) NOT NULL,
  description LONGTEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  type VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  current_revision_no INT UNSIGNED NULL,
  lock_version INT UNSIGNED NOT NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  PRIMARY KEY (scope_id, id),
  KEY idx_plugin_scope_updated (scope_id, updated_at DESC, id DESC),
  CONSTRAINT chk_plugin_type CHECK (type IN ('expert', 'expert_team', 'skill', 'connector')),
  CONSTRAINT chk_plugin_status CHECK (status IN ('ACTIVE', 'ARCHIVED')),
  CONSTRAINT chk_plugin_lock CHECK (lock_version > 0),
  CONSTRAINT chk_plugin_time CHECK (updated_at >= created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- statement
CREATE TABLE IF NOT EXISTS plugin_revision (
  scope_id VARCHAR(40) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  plugin_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  revision_no INT UNSIGNED NOT NULL,
  manifest_json LONGTEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  plugin_json LONGTEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  plugin_hash CHAR(71) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  created_by VARCHAR(191) NOT NULL,
  created_at DATETIME(6) NOT NULL,
  PRIMARY KEY (scope_id, plugin_id, revision_no),
  CONSTRAINT fk_plugin_revision_plugin FOREIGN KEY (scope_id, plugin_id)
    REFERENCES plugin (scope_id, id) ON DELETE RESTRICT,
  CONSTRAINT chk_plugin_revision_no CHECK (revision_no > 0),
  CONSTRAINT chk_plugin_revision_plugin_hash CHECK (REGEXP_LIKE(plugin_hash, '^sha256:[0-9a-f]{64}$', 'c'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- statement
CREATE TABLE IF NOT EXISTS plugin_relation (
  scope_id VARCHAR(40) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  source_plugin_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  relation_type VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  target_plugin_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  PRIMARY KEY (scope_id, source_plugin_id, relation_type, target_plugin_id),
  KEY idx_plugin_relation_target (scope_id, target_plugin_id),
  CONSTRAINT fk_plugin_relation_source FOREIGN KEY (scope_id, source_plugin_id)
    REFERENCES plugin (scope_id, id) ON DELETE CASCADE,
  CONSTRAINT fk_plugin_relation_target FOREIGN KEY (scope_id, target_plugin_id)
    REFERENCES plugin (scope_id, id) ON DELETE RESTRICT,
  CONSTRAINT chk_plugin_relation_type CHECK (
    relation_type IN ('expert_team_expert', 'expert_skill', 'expert_connector')
  )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- statement
ALTER TABLE plugin
  ADD CONSTRAINT fk_plugin_current_revision
  FOREIGN KEY (scope_id, id, current_revision_no)
  REFERENCES plugin_revision (scope_id, plugin_id, revision_no)
  ON DELETE RESTRICT;
