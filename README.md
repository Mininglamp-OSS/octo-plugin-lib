# octo-plugin-lib

`octo-plugin-lib` 是供 Market、Loop、Buddy 及其他 Go 宿主进程直接引用的公共 Plugin
领域库。它统一四类 Plugin 的 JSON Schema、规范化与 Hash 规则，以及宿主私有 MySQL
中的三表 CRUD/Revision/Relation 实现；它不是独立服务，不提供 HTTP、鉴权、审计或对象存储。

## 公共能力

- Plugin 类型：`expert`、`skill`、`expert_team`、`connector`
- 当前状态：`ACTIVE`、`ARCHIVED`
- 当前关系：`expert_team_expert`、`expert_skill`、`expert_connector`
- 内容 Revision：每个 Plugin 内以 `revision_no` 从 1 递增
- 三表存储：`plugin`、`plugin_revision`、`plugin_relation`
- 原子创建单 Plugin 或闭合依赖图、内容更新、状态 CAS、关系替换、历史恢复
- Draft 2020-12 Schema、跨语言 fixtures、Canonical JSON 与 `plugin_hash`
- MySQL 8 安装、精确结构指纹和可复用 conformance suite

## 使用边界

宿主负责身份、权限、Workspace/Space、HTTP、审计、幂等、Market 发布流程、Connector
Secret、Runtime 和对象存储。Attachment 的 `storage` 形式只携带
`path/mime_type/content_size/content_hash`；宿主按 `scope_id + content_hash`
定位自己拥有的对象，公共库不读取 storage 对象字节。

MySQL DSN 必须包含 `parseTime=true&loc=UTC`。安装和使用示例见
[docs/usage.md](docs/usage.md)，完整设计见 [docs/TRD.md](docs/TRD.md)。
`Install` 会实际往返一个带微秒的 UTC 时间并拒绝错误连接配置。通过 `WithTx` 组合宿主
事务时，每次 Lib 写操作由 SAVEPOINT 隔离；宿主仍拥有最终 commit/rollback。

## 验证

```bash
make verify
# 同时设置 OCTO_PLUGIN_LIB_MYSQL_DSN 与 OCTO_PLUGIN_LIB_MYSQL_DRIFT_DSN：
make test-mysql
```

Canonical JSON 是本项目定义的稳定编码，不等同于 RFC 8785/JCS；跨语言实现必须以
`contracts/v2/fixtures/golden` 的 golden 数据为准。对象键按 Unicode scalar 顺序排列，
字符串使用 Go `encoding/json` 转义规则，JSON 最多嵌套 512 层。文档和附件总量由各宿主
入口按自身产品边界限制。发布首个稳定版本后，同一 Schema ID 的内容不可变。
