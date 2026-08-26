# Market 接入 Plugin Lib

Market 是 Space 公开 Plugin 的创建与管理宿主。它可以引用 `octo-plugin-lib` 的公共
Schema、校验、Hash、三表 MySQL Store、CRUD/Revision Service 和 conformance suite，
同时继续独立拥有商品分类、审核、发布、上下架、运营、指标、HTTP、鉴权与审计。

## 数据边界

- Market、Loop、Buddy 使用各自的 database/schema、账号和对象存储所有权。
- 公共表只有 `plugin`、`plugin_revision`、`plugin_relation`。
- Market 私有发布状态不得写入公共 `status`；公共状态只有 `ACTIVE/ARCHIVED`。
- `plugin_relation` 保存商品当前成立的专家团→专家、专家→Skill/Connector 关系。
- 无 `plugin_file` 和 `storage_uri`；Market 自己按
  `scope_id + content_hash` 定位对象。
- `cowork_plugin_audit_log` 不属于 Lib。

## 招募

Market UI 发起“招募”时，Market 后端读取自己的当前闭合关系图和各节点当前 Revision，
把附件原始字节上传到目标宿主票据，再调用目标宿主的图级招募 API。目标宿主创建新的
Plugin ID、Revision 和当前 Relation，持有独立深拷贝。

Lib 不定义跨服务 HTTP DTO，不让 Loop 直连 Market 数据库/Bucket，也不实现 Loop/Buddy
发布回 Market。Market 后续修改、下架或删除不会自动改变已招募副本。

## 接入门禁

1. 使用 `contracts/v2` 和 `contracts/revision/v2`。
2. 运行 `pluginconformance.Run` 覆盖 CRUD、CAS、Revision、关系和图原子性。
3. MySQL DSN 使用 `parseTime=true&loc=UTC`，启动时执行 `VerifySchema`。
4. 通过 JSON Schema invalid fixtures、`fixtures/semantic/invalid.json` 和 golden Hash。
5. 在 Market 私有层验证 Space 可见性、发布状态、对象字节和调用身份。
