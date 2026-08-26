# 使用说明

## 安装

```bash
go get github.com/Mininglamp-OSS/octo-plugin-lib@<version>
```

MySQL 8 DSN 必须包含 `parseTime=true&loc=UTC`。

## 初始化

```go
db, err := sql.Open("mysql", dsn)
if err != nil { return err }
if err := mysqlstore.Install(ctx, db); err != nil { return err }
store, err := mysqlstore.New(db)
if err != nil { return err }
plugins, err := pluginservice.New(store)
```

`Install` 只适用于新空 schema 或已精确匹配的三表结构；`VerifySchema` 会拒绝列、约束、
索引或表漂移。

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

archived, err := plugins.SetStatus(ctx, scope, actor, pluginID, pluginservice.SetStatusInput{
    ExpectedLockVersion: updated.Plugin.LockVersion,
    Status: plugin.StatusArchived,
})
```

外部 JSON 先通过 `plugin.DecodePlugin`、`plugin.DecodeRelation` 或
`plugin.DecodeRevisionContent` 严格解码；这些入口会拒绝未知字段，并执行 Schema 无法
表达的跨字段、Hash 和时间语义。只有已经由宿主组装成强类型值时才直接调用 `Validate*`。

创建固定为 `ACTIVE`。内容、状态和关系各自独立修改，但共享 `lock_version`；客户端必须
使用最近一次读取的值处理 CAS 冲突。

关系通过 `ReplaceRelations` 整体替换。闭合依赖图通过 `CreateGraph` 一次写入，禁止
逐项 Create 加失败补偿冒充原子操作。历史通过 `GetRevision(pluginID, revisionNo)` 和
`Restore(pluginID, revisionNo, ...)` 使用；Restore 创建新 Revision 并保留当前关系。

## Attachment

raw 文件直接放 `raw_content`。storage 文件只提交 `content_size/content_hash`；宿主
先验证原始字节，再按 `scope_id + content_hash` 存入自己的 Content Store。公共契约没有
`storage_uri`，也不读取 storage 对象字节。

路径最多 512 字符并拒绝绝对路径、Windows 盘符路径、反斜杠、dot/parent/空段和重复
path。文本空白使用 Unicode White_Space。`content_size` 最大
`9007199254740991`。

四类 Plugin 都允许显式 `plugin_json: null`。非空 Package 中，Expert 需要 `AGENTS.md`，
Skill 需要 `SKILL.md`，Expert Team 只能包含 `AGENTS.md`；Connector 必须有描述对象并按
类型提供 `mcp.json`、`cli.json + skills/<name>/SKILL.md` 或
`token-schema.json + skills/<name>/SKILL.md`。附件数组顺序不表达业务语义。

Canonical JSON 不等同于 RFC 8785/JCS；其他语言必须跑 Schema、semantic fixtures 和
golden Hash，而不是重新猜算法。孤立 UTF-16 surrogate 转义会被拒绝。
