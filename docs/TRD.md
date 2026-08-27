# Octo Plugin Lib 技术设计

## 1. 目标与边界

`octo-plugin-lib` 在 Go 宿主进程内提供公共 Plugin 契约、校验、Hash、三表 MySQL Store、
CRUD/Revision Service 和 conformance suite。Market、Loop、Buddy 以及其他宿主引用同一
库，但各自拥有 database/schema、账号、对象存储、HTTP、鉴权、审计和业务生命周期。

本库不部署，不提供网络服务，也不包含 Workspace/Space、Market 发布审核、跨宿主传输封装、
市场版本与来源、Connector Secret、Runtime 或 `cowork_plugin_audit_log`。

## 2. 领域契约

Plugin 类型为 `expert`、`skill`、`expert_team`、`connector`；可用状态为
`ACTIVE`、`ARCHIVED`。创建固定为 ACTIVE。内容修改、状态修改和关系修改分别使用
同一个 `lock_version` 做乐观并发控制。
只有新建内容 Revision 的 Create、Update、Restore 和 CreateGraph 接收 Actor 并写入
`created_by`；SetStatus 与 ReplaceRelations 的操作人授权和审计由宿主拥有。

关系只有：

- `expert_team_expert`：Expert Team → Expert
- `expert_skill`：Expert → Skill
- `expert_connector`：Expert → Connector

Relation 表示当前绑定，不属于 Revision。关系变更不创建 Revision，Restore 只把历史
内容追加为新 Revision，不恢复历史关系。新关系要求 source 和 target 都是 ACTIVE；已存在
关系可在目标归档后保留，也可从归档 source 删除。

ARCHIVED Plugin 的普通 Update 一律返回冲突，包括内容相同的 no-op Update。Restore 是唯一
例外：它把指定历史内容追加为新 Revision，但保持 Plugin 为 ARCHIVED；调用方必须再通过
独立 status-only CAS 显式恢复 ACTIVE。

## 3. 机器契约

| Schema | ID |
| --- | --- |
| Plugin | `cowork-plugin-2.0.json` |
| Manifest | `cowork-plugin-manifest-2.0.json` |
| Package | `cowork-plugin-package-2.0.json` |
| Relation | `cowork-plugin-relation-2.0.json` |
| Revision Content | `cowork-plugin-revision-content-2.0.json` |

Plugin 字段：`plugin_id`、`plugin_name`、`plugin_type`、`manifest_json`、
`plugin_json`、`plugin_hash`、`status`、`created_at`、`updated_at`。

Relation 字段：`source_plugin_id`、`target_plugin_id`、`relation_type`。

Revision Content 字段：`plugin_type`、`manifest_json`、`plugin_json`、
`plugin_hash`。它不包含关系、Revision 序号、作者和时间。

JSON Schema 校验结构；`fixtures/semantic/invalid.json` 固化 Schema 无法表达的跨字段规则，
包括 Plugin/Manifest 名称与类型一致、Hash 一致、时间顺序、Relation 非自环与端点类型、
以及按 Plugin 类型约束的 Package 文件形态。Go 调用方使用 `DecodePlugin`、
`DecodeRelation`、`DecodeRevisionContent` 严格拒绝未知字段并执行对应语义校验。

稳定错误码：`invalid_json`、`invalid_field`、`invalid_plugin_type`、
`invalid_relation_type`、`invalid_relation`、`invalid_attachment_path`、
`duplicate_attachment_path`、`invalid_attachment_content`、`hash_mismatch`。

已发布的同一 Schema ID 内容不可变。

## 4. Attachment 与 Hash

- path 最长 512 字符，必须是规范化相对 slash 路径；拒绝绝对路径、Windows 盘符路径、
  反斜杠、dot/parent/空段、控制字符、重复路径和目录式尾斜杠。
- MIME 最长 255 字符；非空文本按 Unicode White_Space 判断。
- raw 只携带 UTF-8 `raw_content`，禁止 `content_size/content_hash`。
- storage 只携带 `content_size/content_hash`，禁止 `raw_content/storage_uri`。
  `content_size` 范围是 0～`9007199254740991`。
- Lib 不读取 storage 对象字节；宿主必须在进入 Lib 前完成字节上传和摘要核验。
- 公共契约不冻结文档总大小、附件总大小或附件数量；宿主必须在 HTTP、上传和存储边界
  限制不可信输入，不能把 Lib 当作请求体限流器。

四类 Plugin 均允许显式 `plugin_json: null`。非空 Package 的最小形态为：Expert 必须有
`AGENTS.md` 且无 Connector 描述；Skill 必须有 `SKILL.md` 且无 Connector 描述；Expert
Team 只能有 `AGENTS.md`；Connector 必须有 Connector 描述，且 `mcp/openconnector` 需要
`mcp.json`，`cli` 需要 `cli.json` 与嵌套 Skill，`skill-only` 需要 `token-schema.json`
与嵌套 Skill。附件顺序没有业务语义。

`plugin_hash = sha256(canonical(manifest_json) + canonical(plugin_json))`。
Attachment 按 path 排序，所以其数组顺序不改变 Hash；其他数组保持顺序。Canonical JSON
不等同于 RFC 8785/JCS。数字词法及规范化结果最长 10,240 字符，指数范围为 -10000～10000；
一份 JSON 文档累计数字展开增量最多 10,240 字符，避免少量指数词法放大为无界内存；
对象键排序为
Unicode scalar value 升序；对合法 UTF-8 等价于 UTF-8 字节序，不得使用 UTF-16 code-unit
默认顺序。字符串使用 Go `encoding/json` 的转义规则，`<`、`>`、`&`、U+2028、
U+2029 输出为 `\u` 转义。对象排序为 O(k log k)，内存与规范化输出之和同阶；JSON
最多嵌套 512 个 object/array 容器。UUID 只校验
小写文本形状，不校验 UUID version 或 variant。孤立 UTF-16 surrogate 转义被拒绝，避免
跨语言 Hash 分叉。跨语言实现必须同时执行 JSON Schema、semantic fixtures 和 golden Hash。

## 5. 持久化

物理表和键见 [er.md](er.md)。只有三张表：

- `plugin`：当前名称与类型查询投影、状态、Revision 指针与 CAS 版本；
- `plugin_revision`：不可变内容历史；
- `plugin_relation`：当前关系集合。

`scope_id VARCHAR(40)` 是租户分区键，不代表授权。Plugin 主表在实体内部使用
`id/name/type`，引用边界使用 `plugin_id/source_plugin_id/target_plugin_id`。
`current_revision_no` 是三列复合外键的一部分；`revision_no` 和 `lock_version`
都是 `INT UNSIGNED`，Go 使用 `uint32`。

跨层字段矩阵：

| 事实 | JSON / Service | Go | MySQL | 权威与校验 |
| --- | --- | --- | --- | --- |
| 宿主隔离键 | `Scope.ID` | `string`，1～40 位 ASCII 标识符 | `plugin.scope_id VARCHAR(40) ASCII BIN` | 宿主映射；Contract、Service、Store、主键、外键和 CHECK 共同阻止空值、非法字符及跨 scope 关系 |
| Plugin 身份 | `plugin_id` / `CreateInput.PluginID` | 小写 UUID 文本 | `plugin.id CHAR(36) ASCII BIN` | 宿主生成；Go 与 CHECK 使用同一形状规则，不校验 UUID version/variant |
| 当前名称/类型 | `plugin_name/plugin_type` 与 Manifest | `string` / `plugin.Type` | `plugin.name/type` | Manifest 是内容真源；Service 同事务维护查询投影，读取交叉校验 |
| 当前状态 | `ACTIVE/ARCHIVED` | `plugin.Status` | `plugin.status VARCHAR(16) ASCII BIN` | Service 转换；CHECK 拒绝其它状态 |
| 内容 Revision | Revision Content | `pluginstore.Revision` | `plugin_revision` | 每 Plugin 的 `revision_no` 单调递增，内容不可变 |
| 当前 Revision | Snapshot | `uint32` | `current_revision_no INT UNSIGNED` | 三列复合外键；创建事务中可暂时为 NULL，提交后必须存在 |
| 并发版本 | `expected_lock_version` 由宿主 API 映射 | `uint32` | `lock_version INT UNSIGNED` | 内容、状态和关系共用 CAS，不等同于 Revision No |
| 内容文档/摘要 | `manifest_json/plugin_json/plugin_hash` | `json.RawMessage/string` | `LONGTEXT utf8mb4_bin` / `CHAR(71) ASCII BIN` | Lib Canonical JSON 与 SHA-256 是唯一写入真源，读取复核 |
| 当前关系 | Relation Schema / `RelationInput` | `pluginstore.Relation` | `plugin_relation` | source Plugin 当前态拥有；复合外键禁止跨 scope，不进入 Revision/Hash |
| Revision 作者 | `Actor.ID` | `string`，1～191 位 ASCII 标识符 | `created_by VARCHAR(191) ASCII BIN` | 仅内容 Revision 记录；Contract、Service、Store 与 CHECK 使用同一规则，宿主负责身份与审计 |

创建在一个事务中插入 Plugin 占位行、Revision 1、回填当前指针和当前关系。内容更新锁定
Plugin 行，按 `current_revision_no + 1` 分配序号并切换指针。图创建先完整校验闭包，
再在一个事务内写入所有节点和关系；任何失败全部回滚。时间统一为 UTC 微秒，MySQL DSN
必须含 `parseTime=true&loc=UTC`；`Install` 通过已知 UTC 微秒时间往返检查该配置并 fail
closed。安装前先在数据库级 advisory lock 内检查三张自有表：三表全无时才执行基线 DDL，
三表齐全时只做精确结构校验，存在 1～2 张表或任一结构漂移时拒绝启动，不补表、不补约束。
锁释放使用独立的 5 秒清理上下文并校验数据库确实返回已释放；释放失败时丢弃该物理连接，
避免 advisory lock 随连接回到连接池。当前结构指纹已在 MySQL 8.0.46 和 8.4.10 验证。

`Get`、`List` 和 `ListRevisions` 的非绑定调用在 `REPEATABLE READ` 快照事务中完成各自
所有 SQL。事务不声明 `READ ONLY`，避免数据库代理把需要读己之写的请求误路由到延迟副本；
宿主连接仍必须指向满足读己之写的节点。`Get` 不会在并发关系替换时混合旧
`lock_version` 与新 Relation；两个列表的总数和当前页也来自同一个数据库快照。
`WithTx` 绑定读取遵循宿主事务的隔离级别和快照。
传入 `WithTx` 的事务必须由初始化该 Store 的同一 database/schema 连接池创建；`database/sql`
不公开可可靠校验这一归属的元数据，因此该装配约束由宿主负责。

Revision 不可变由公开 Service/Store API 不提供 Update/Delete 保证；Lib 不创建数据库
trigger。宿主必须限制应用数据库账号及原始 SQL 权限，不能绕过 Lib 改写历史。
`manifest_json/plugin_json` 使用 `LONGTEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin` 保存
Lib 生成的 Canonical JSON。这里不用 MySQL 原生 JSON：公共 Manifest 允许扩展字段，
Canonical JSON 支持精确十进制；原生 JSON 的二进制重编码可能改变超出其原生数值范围的
合法数字。`VARCHAR` 又受行大小限制，不能承载宿主允许的大型 Plugin 文档。MySQL 的
`JSON_VALID/JSON_EXTRACT` 也使用同一个受限数值解析器，所以 DDL 不以它们缩窄公共契约。
写入必须通过 Lib 完成结构、语义和 Hash 校验；`Get/GetRevision` 完整内容读取时重新校验
Canonical 字节、公共契约和 `plugin_hash`，数据库内容被绕过 Store 改写、无法通过公共契约
校验或与 Hash 不一致时返回 `INTEGRITY_FAILURE`，不得把损坏内容交给宿主。`List` 不读取可能
很大的 `plugin_json`，只校验查询命中并返回行的 Manifest 为 Canonical 且与主表名称、
类型投影一致；`ListRevisions` 只返回元数据，调用方使用内容或 Hash 前必须调用
`GetRevision`。生产账号必须依靠最小权限阻止绕过 Store；发现损坏时停写并由管理员
从可信来源重建，不提供在线猜测修复。Manifest 是 description 的唯一真源；`plugin` 表不保存
description 投影，List 不支持描述搜索。
生产环境应分离迁移账号与运行时账号；运行时只授予三表 CRUD 实际需要的最小权限，不授予
Revision UPDATE/DELETE、Plugin DELETE 或 DDL 权限。调用方最低 Go 语言版本为 1.25；本仓库
使用 Go 1.25.11 工具链验证。

列表按 `scope_id, updated_at DESC, id DESC` 分页；名称支持字面 LIKE 搜索，不搜索 Manifest description。
单次调用内的总数和当前页一致，但页码分页不是跨请求快照：分页期间发生更新时，
项目可能换页，调用方应刷新列表；没有实际产品需求和测量证据前不增加游标协议。
`idx_plugin_scope_updated` 是当前唯一业务辅助索引，其他索引只为主外键服务。

## 6. Go API

| API | 作用 |
| --- | --- |
| `contracts.Schema` / `contracts.Fixture` / `contracts.RevisionFixture` | 读取嵌入契约 |
| `plugin.DecodePlugin` / `plugin.DecodeRelation` / `plugin.DecodeRevisionContent` | 严格解码公共文档并执行语义校验 |
| `plugin.DecodeManifest` / `plugin.DecodePackage` | 解析并验证内容 |
| `plugin.ValidatePluginID` / `plugin.ValidateStatus` | 验证公共值 |
| `plugin.ValidatePlugin` / `plugin.NormalizePlugin` | 校验/规范化完整 Plugin |
| `plugin.ValidateRelation` / `plugin.ValidateRelationEndpoints` | 校验关系 |
| `plugin.CanonicalJSON` / `plugin.ComputePluginHash` | 稳定编码与内容 Hash |
| `plugin.ValidateRevisionContent` / `plugin.NormalizeRevisionContent` | Revision 内容契约 |
| `plugin.Type.Valid` / `plugin.RelationType.Valid` | 枚举检查 |
| `plugin.IsValidStatus` / `plugin.IsActiveStatus` | 状态检查 |
| `pluginstore.Code` | 稳定存储错误分类 |
| `mysqlstore.SchemaSQL` / `mysqlstore.Install` / `mysqlstore.VerifySchema` / `mysqlstore.New` | MySQL 接入 |
| `Store.WithTx` | 以每次写调用一个 SAVEPOINT 加入宿主事务 |
| `pluginservice.New` / `Service.WithTx` | Service 构造与事务绑定 |
| `Service.Create` / `Service.CreateGraph` | 原子创建 |
| `Service.Get` / `Service.List` | 当前态读取 |
| `Service.Update` | 内容更新 |
| `Service.SetStatus` | 状态 CAS |
| `Service.ReplaceRelations` | 当前关系整体替换 |
| `Service.GetRevision` / `Service.ListRevisions` / `Service.Restore` | 历史内容 |
| `pluginconformance.Run` | 仅供宿主 `_test.go` 使用的一致性测试 |

## 7. 错误与恢复

Store 对外稳定分类为 `INVALID_ARGUMENT/NOT_FOUND/ALREADY_EXISTS/CONFLICT/`
`INTEGRITY_FAILURE/INTERNAL`。CAS 冲突、死锁和锁等待超时归为 `CONFLICT`；主键冲突归为
`ALREADY_EXISTS`；外键和 CHECK 失败归为 `INTEGRITY_FAILURE`。失败的自管事务全部回滚。
Service 返回契约校验错误时同时匹配 `pluginstore.ErrInvalidArgument` 和原始
`plugin.ValidationError`；宿主可安全读取稳定的 `Code/Path`，不能解析错误文本做流程判断。
`WithTx` 仍由宿主最终提交或回滚，但每次 Lib 写调用建立 SAVEPOINT；调用失败只回滚该次
调用，宿主之前的写入仍可继续使用。若 SAVEPOINT 回滚或释放失败，Lib 主动回滚整个宿主
事务并返回匹配 `pluginstore.ErrTransactionAborted` 的错误，此时宿主必须重开事务，不能重试
或提交旧事务。该错误即使同时包含冲突原因，`pluginstore.Code` 也固定返回 `INTERNAL`，
避免宿主把已终止事务当作普通 CAS 冲突继续使用。MySQL 死锁可能由数据库直接回滚整个
事务，返回的错误同样匹配该 sentinel。

本库不执行推断式迁移或结构修补。宿主只可在新空 database/schema 安装，或连接精确匹配的
三表结构；只存在部分表、或三表已建但最终外键未完成时都拒绝启动。若能证明这是首次安装
中断且三表尚无业务数据，可删除三张不完整表后重新执行 `Install`；否则使用新空库并从可信
原始内容通过当前 Lib 重新校验导入，不做可能损坏 Canonical 字节的原地列转换。

## 8. 明确开放项

以下事项没有多宿主共识，不属于当前 v2 的变更承诺，也不得由单个接入方私自改写：

- 公共 JSON/API 继续使用 `plugin_id/plugin_name/plugin_type`；物理主表使用 `id/name/type`。
- Manifest 暂时同时保留 `plugin_name/plugin_type/name`；它们的最终单一真源仍待确认。
- `created_by` 当前上限为 191；只允许 ASCII 标识符，但具体主体格式和更窄长度未冻结。
- Connector `source` 保持必填；删除或改名需先统一跨宿主描述格式。
- `plugin_json` 继续表示 Package；是否改名为 `package_json` 需单独升级机器契约。

任何一项确认变更后都必须使用新的 Schema ID，并同步 Go API、fixtures、DDL/Store、
conformance 与全部文档；不能原地修改已经发布的 Schema。

## 9. 验收

- JSON Schema、Go 语义校验、fixtures、golden Hash 和字段漂移测试通过；
- Python 标准库独立复算 Canonical JSON 边界与 golden Hash，不依赖 Go 实现；
- unit、race、vet、build 通过；
- MySQL 8.0.46、8.4.10 fresh install、重复 Install、CRUD、CAS、SAVEPOINT 回滚、scope
  隔离、图原子性和结构漂移检测通过；
- 独立 `GOWORK=off` 消费者可下载并编译正式版本。
