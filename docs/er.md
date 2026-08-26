# Plugin Lib ER

API/Schema 使用 `plugin_id/plugin_name/plugin_type`；持久化到 `plugin` 表时分别对应
上下文明确的 `id/name/type`。Plugin 类型只允许 `expert`、`skill`、`expert_team`、
`connector`，状态只允许 `ACTIVE`、`ARCHIVED`。Relation 类型只允许
`expert_team_expert`、`expert_skill`、`expert_connector`。

```mermaid
erDiagram
    plugin ||--|{ plugin_revision : owns
    plugin ||--o| plugin_revision : current
    plugin ||--o{ plugin_relation : source
    plugin ||--o{ plugin_relation : target

    plugin {
        varchar_40 scope_id PK
        char_36 id PK
        varchar_160 name
        varchar_16 type
        varchar_16 status
        int_unsigned current_revision_no FK
        int_unsigned lock_version
        datetime_6 created_at
        datetime_6 updated_at
    }

    plugin_revision {
        varchar_40 scope_id PK,FK
        char_36 plugin_id PK,FK
        int_unsigned revision_no PK
        json manifest_json
        json plugin_json
        char_71 plugin_hash
        varchar_191 created_by
        datetime_6 created_at
    }

    plugin_relation {
        varchar_40 scope_id PK,FK
        char_36 source_plugin_id PK,FK
        varchar_32 relation_type PK
        char_36 target_plugin_id PK,FK
    }
```

## 约束

- `plugin` 主键：`(scope_id, id)`。
- `plugin_revision` 主键：`(scope_id, plugin_id, revision_no)`；外键指向
  `plugin(scope_id,id)`。
- 当前 Revision 是一个复合外键：
  `plugin(scope_id,id,current_revision_no) -> plugin_revision(scope_id,plugin_id,revision_no)`。
  `current_revision_no` 在建档事务中可暂时为 NULL，提交后的 Plugin 必须有当前 Revision。
- `plugin_relation` 主键：
  `(scope_id,source_plugin_id,relation_type,target_plugin_id)`；source/target 都通过
  包含 `scope_id` 的外键指向 `plugin`，数据库阻止跨 scope 关系。
- `revision_no` 由 Lib 在锁定 Plugin 行的事务内按当前值加一；不使用全表
  `AUTO_INCREMENT`。
- `lock_version` 覆盖内容、状态和关系三类当前态修改。
- Relation 是当前绑定，不属于 Revision；Restore 只恢复内容，不回滚关系。
- 无 `plugin_file` 表。Attachment 的物理位置由宿主 Content Store 根据
  `scope_id + content_hash` 解析。
