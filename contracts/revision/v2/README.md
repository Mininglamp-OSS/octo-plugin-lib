# Plugin Revision Content v2

`cowork-plugin-revision-content-2.0.json` 只有四个不可变内容字段：

- `plugin_type`
- `manifest_json`
- `plugin_json`
- `plugin_hash`

`revision_no`、作者和时间是持久化元数据，不进入内容契约。Relation 表达 Plugin 当前
绑定，不属于 Revision，因此本契约没有 `relations` 或 `revision_hash`。恢复历史内容
会创建新的 `revision_no`，同时保留当时的当前 Relation。

`plugin_hash` 使用与基础契约相同的 Canonical JSON 与 Attachment path 排序规则。
Schema 负责结构，`contracts/v2/fixtures/semantic/invalid.json` 负责跨字段语义案例；Go
调用方可直接使用 `plugin.DecodeRevisionContent` 完成严格解码、类型语义和 Hash 校验。
独立加载本 Schema 时必须同时注册它引用的 Manifest 与 Package Schema。发布首个稳定
版本后，同一 Schema ID 的内容不可变。
