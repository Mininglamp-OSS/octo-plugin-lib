# 使用说明

## 安装

```bash
go get github.com/Mininglamp-OSS/octo-plugin-lib@<version>
```

调用方最低 Go 语言版本为 1.25；本仓库建议使用 Go 1.25.11 工具链验证。
MySQL 8 DSN 必须包含 `parseTime=true&loc=UTC`。`Install` 会实际往返一个 UTC 微秒时间，
错误或会截断时间的连接配置会直接失败。

## 初始化

```go
db, err := sql.Open("mysql", dsn)
if err != nil { return err }
if err := mysqlstore.Install(ctx, db); err != nil { return err }
store, err := mysqlstore.New(db)
if err != nil { return err }
plugins, err := pluginservice.New(store)
```

`Install` 只适用于新空 schema 或已精确匹配的三表结构：三张自有表全无时才安装，三表齐全
时只校验；只存在 1～2 张表，或三表已建但最终外键尚未完成时，都会直接失败，不猜测性
补表或补约束。若确认这是首次安装中断且三表尚无业务数据，删除这三张不完整表后重新执行
`Install`；不能证明为空时使用新空库并从可信内容重建。`VerifySchema` 会拒绝列、
约束、索引或表漂移。安装过程由数据库级 advisory lock 串行化，锁释放使用独立 5 秒清理
超时并校验释放结果；释放失败时连接会被丢弃，不能带锁回到连接池。不兼容结构必须使用
新空库，并从可信原始内容通过当前 Lib 重新校验导入；不得用原地列转换冒充安全迁移。
生产环境可用迁移账号执行 `Install/VerifySchema`，再把运行时连接交给 `New`。运行时账号
只需 Plugin 表的 SELECT/INSERT/UPDATE、Revision 表的 SELECT/INSERT，以及 Relation 表的
SELECT/INSERT/DELETE；不应拥有 Revision UPDATE/DELETE、Plugin DELETE 或 DDL 权限。
`scope_id` 和 `created_by` 只接受非空 ASCII 标识符（字母、数字、点、下划线、冒号、
连字符），Plugin ID 只接受小写 UUID 文本形状；Contract、Service、Store 与 MySQL CHECK
使用同一规则。

真实 MySQL 门禁使用 `make test-mysql`；它要求同时提供
`OCTO_PLUGIN_LIB_MYSQL_DSN` 与 `OCTO_PLUGIN_LIB_MYSQL_DRIFT_DSN`，缺失时失败而不是跳过。
`make verify` 还会通过 Python 标准库独立执行 Canonical JSON 与 golden Hash
跨语言门禁；它不是生产运行时依赖。

## 创建与修改

```go
created, err := plugins.Create(ctx, pluginservice.Scope{ID: workspaceID},
    pluginservice.Actor{ID: userID}, pluginservice.CreateInput{
        PluginID: pluginID,
        Content: pluginservice.ContentInput{
            PluginType: plugin.TypeSkill,
            ManifestJSON: manifest,
            PluginJSON: packageJSON,
        },
    })

updated, err := plugins.Update(ctx, scope, actor, pluginID, pluginservice.UpdateInput{
    ExpectedLockVersion: created.Plugin.LockVersion,
    Content: nextContent,
})

archived, err := plugins.SetStatus(ctx, scope, pluginID, pluginservice.SetStatusInput{
    ExpectedLockVersion: updated.Plugin.LockVersion,
    Status: plugin.StatusArchived,
})
```

外部 JSON 先通过 `plugin.DecodePlugin`、`plugin.DecodeRelation` 或
`plugin.DecodeRevisionContent` 严格解码；这些入口会拒绝未知字段，并执行 Schema 无法
表达的跨字段、Hash 和时间语义。只有已经由宿主组装成强类型值时才直接调用 `Validate*`。
Service 的契约校验错误同时匹配 `pluginstore.ErrInvalidArgument` 和原始
`*plugin.ValidationError`；宿主通过 `errors.As` 读取稳定 `Code/Path`，不要解析错误文本。

创建固定为 `ACTIVE`。内容、状态和关系各自独立修改，但共享 `lock_version`；客户端必须
使用最近一次读取的值处理 CAS 冲突。
只有会写入 Revision `created_by` 的 Create、Update、Restore 和 CreateGraph 接收
`Actor`；SetStatus 和 ReplaceRelations 的操作人审计由宿主负责，Lib 不接收或伪装保存它。

ARCHIVED Plugin 不接受普通 Update，即使提交内容与当前内容完全相同。Restore 仍可把历史
内容追加为新 Revision，但状态保持 ARCHIVED；需要恢复使用时再显式 `SetStatus(ACTIVE)`。

关系通过 `ReplaceRelations` 整体替换。闭合依赖图通过 `CreateGraph` 一次写入，禁止
逐项 Create 加失败补偿冒充原子操作。历史通过 `GetRevision(pluginID, revisionNo)` 和
`Restore(pluginID, revisionNo, ...)` 使用；Restore 创建新 Revision 并保留当前关系。

`List` 使用页码/offset，并按 `updated_at DESC, id DESC` 排序。单次调用的总数与当前页
来自同一快照，但不提供跨多个请求的快照；分页期间发生更新时项目可能换页，调用方应刷新。

`Get`、`List` 和 `ListRevisions` 的普通调用使用 `REPEATABLE READ` 快照事务；事务不声明
`READ ONLY`，避免数据库代理把需要读己之写的请求误路由到延迟副本。宿主连接必须指向满足
读己之写的节点。`Get` 返回 Plugin、当前 Revision 与当前 Relation 的一致快照，两个列表的
总数和当前页也不会混用两个时刻。`WithTx` 下的读取遵循调用方事务的隔离级别。

`manifest_json/plugin_json` 以 `utf8mb4_bin LONGTEXT` 保存 Lib 生成的 Canonical JSON，
精确保留公共扩展字段中的十进制数字，并避免 `VARCHAR` 的行大小上限。MySQL
`JSON_VALID/JSON_EXTRACT` 会拒绝部分公共 Canonical 数字，因此不作为持久化校验边界；宿主
只能通过 Lib 写入。`Get/GetRevision` 重新校验 Canonical 字节、公共契约并用 `plugin_hash`
检查完整 Revision 内容；检测到数据库内容漂移时返回 `pluginstore.ErrIntegrity`，宿主不能继续
使用该内容。`List` 为避免读取每个 Plugin 的大文件树，只校验查询命中并返回行的 Manifest
Canonical 形式及其与主表名称、类型投影的一致性。`description` 只保留在 Manifest 中；
`plugin` 表不保存该投影，`List` 的 Query 只搜索名称。`ListRevisions` 只返回历史元数据，
使用内容或 Hash 前调用 `GetRevision` 完整校验。
发现数据漂移时停止写入，由管理员从可信内容重建新库；不要为绕过最小权限修改的损坏数据
增加在线猜测修复。运行时账号的最小权限仍是第一道防线。

## 与宿主事务组合

```go
tx, err := db.BeginTx(ctx, nil)
if err != nil { return err }
defer tx.Rollback()

bound, err := plugins.WithTx(tx)
if err != nil { return err }
if _, err = bound.CreateGraph(ctx, scope, actor, graph); err != nil {
    if errors.Is(err, pluginstore.ErrTransactionAborted) {
        return err // 整个事务已回滚，必须重新开始
    }
    return err // 本次调用已回滚到 SAVEPOINT；由宿主决定是否继续事务
}
// 写入宿主审计、幂等或投影记录。
return tx.Commit()
```

每次绑定事务的 Lib 写调用都有独立 SAVEPOINT。调用失败不会留下半个 Plugin/依赖图；若
SAVEPOINT 无法恢复或 MySQL 已因死锁终止整个事务，错误匹配
`pluginstore.ErrTransactionAborted`，且 `pluginstore.Code` 返回 `INTERNAL`；宿主不得继续
使用该事务。传入 `WithTx` 的事务必须来自初始化 Store 的同一 database/schema 连接池；
`database/sql` 不提供可靠的事务归属反射能力，Lib 无法替宿主自动判断。

## Attachment

raw 文件直接放 `raw_content`。storage 文件只提交 `content_size/content_hash`；宿主
先验证原始字节，再按 `scope_id + content_hash` 存入自己的 Content Store。公共契约没有
`storage_uri`，也不读取 storage 对象字节。

路径最多 512 字符并拒绝绝对路径、Windows 盘符路径、反斜杠、dot/parent/空段和重复
path。文本空白使用 Unicode White_Space。`content_size` 最大
`9007199254740991`。

公共契约不规定整个 JSON、附件总量或附件数量上限；宿主必须在 HTTP、上传和对象存储
入口先限制不可信输入。Revision 的不可变性由 Lib API/Store 不提供 Update/Delete 保证；
宿主数据库账号不得绕过 Lib 用原始 SQL 改写历史。

四类 Plugin 都允许显式 `plugin_json: null`。非空 Package 中，Expert 需要 `AGENTS.md`，
Skill 需要 `SKILL.md`，Expert Team 只能包含 `AGENTS.md`；Connector 必须有描述对象并按
类型提供 `mcp.json`、`cli.json + skills/<name>/SKILL.md` 或
`token-schema.json + skills/<name>/SKILL.md`。附件数组顺序不表达业务语义。

Canonical JSON 不等同于 RFC 8785/JCS；其他语言必须跑 Schema、semantic fixtures 和
golden Hash，而不是重新猜算法。对象键按 Unicode scalar value（合法 UTF-8 字节序）
排列，不得使用 UTF-16 code-unit 默认顺序；字符串沿用 Go `encoding/json`，因此 `<`、
`>`、`&`、U+2028、U+2029 会转义。最多嵌套 512 个 object/array，孤立 UTF-16 surrogate
转义会被拒绝。

`pluginconformance.Run` 只应从宿主的 `_test.go` 调用，不应进入生产二进制。
