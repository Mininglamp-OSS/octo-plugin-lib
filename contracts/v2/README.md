# Plugin 公共契约 v2

## Schema

| 文件 | Schema ID | 用途 |
| --- | --- | --- |
| `cowork-plugin-2.0.json` | `cowork-plugin-2.0.json` | 完整 Plugin |
| `cowork-plugin-manifest-2.0.json` | `cowork-plugin-manifest-2.0.json` | Manifest |
| `cowork-plugin-package-2.0.json` | `cowork-plugin-package-2.0.json` | 文件树和 Connector 描述 |
| `cowork-plugin-relation-2.0.json` | `cowork-plugin-relation-2.0.json` | 当前 Relation |
| `cowork-plugin-revision-content-2.0.json` | `cowork-plugin-revision-content-2.0.json` | 不可变 Revision Content |
| `errors.json` | — | 稳定校验错误码 |

Plugin 的公共字段固定为 `plugin_id/plugin_name/plugin_type/manifest_json/plugin_json/`
`plugin_hash/status/created_at/updated_at`。Relation 固定为
`source_plugin_id/target_plugin_id/relation_type`。

Plugin `status` 只允许 `ACTIVE` 与 `ARCHIVED`；创建时必须为 `ACTIVE`。

## Attachment

- path 是最多 512 字符的规范化相对 slash 路径；拒绝绝对路径、Windows 盘符路径、
  反斜杠、空段、dot/parent 段、控制字符和目录式尾斜杠。
- `mime_type` 最多 255 字符，并使用 Unicode White_Space 语义拒绝空白。
- `raw` 必须有 UTF-8 `raw_content`，禁止 `content_size/content_hash`。
- `storage` 必须有 `content_size/content_hash`，禁止 `raw_content` 和任何
  `storage_uri`；`content_size` 上限为 `9007199254740991`。
- Attachment path 必须唯一。JSON Schema 无法按对象属性表达该唯一性，因此
  `invalid/duplicate-path.json` 是所有语言实现都必须执行的语义 fixture。
- 公共契约不冻结文档总字节数、附件总字节数或附件数量；宿主必须在自己的 HTTP、上传和
  存储入口设置限额，再把数据交给 Lib。

Connector 规则：`mcp` 与 `openconnector` 需要根目录 `mcp.json`；`cli` 需要
`cli.json` 和至少一个 `skills/<name>/SKILL.md`；`skill-only` 必须包含根目录
`token-schema.json`，并至少有一个嵌套 Skill。附件数组顺序不表达业务语义。

四类 Plugin 都允许显式 `plugin_json: null`。非空 Package 中，Expert 必须有根目录
`AGENTS.md` 且不得带 Connector 描述；Skill 必须有根目录 `SKILL.md` 且不得带 Connector
描述；Expert Team 只能包含根目录 `AGENTS.md`；Connector 必须带 Connector 描述并满足
上面的类型规则。

## Hash

`plugin_hash = sha256(canonical(manifest_json) + canonical(plugin_json))`。Attachment 在
Hash 前按 path 排序，数组输入顺序不改变结果；其他数组保留顺序。算法不读取 storage
对象字节，也不包含宿主 Bucket/object key。

Canonical JSON 不等同于 RFC 8785/JCS。单个数字词法及规范化结果最长 10,240 字符，指数
范围为 -10000～10000；一份 JSON 文档累计数字展开增量最多 10,240 字符，避免少量指数
词法放大为无界内存。对象键排序为 O(k log k)，内存与规范化输出之和同阶。
对象键按 Unicode scalar value 升序排列；对合法 UTF-8，这等价于按 UTF-8 字节序排列，
不得直接使用 JavaScript/Java 的 UTF-16 code-unit 默认顺序。字符串按 Go
`encoding/json` 规则编码，其中 `<`、`>`、`&`、U+2028、U+2029 使用 `\u` 转义。
JSON 最多嵌套 512 个 object/array 容器。UUID 只校验小写标准文本形状，不校验 UUID
version 或 variant。孤立 UTF-16 surrogate 转义会被拒绝，避免不同语言替换行为造成
Hash 分叉。

跨语言实现必须先执行 JSON Schema，再执行 `fixtures/semantic/invalid.json` 中按
`validator` 标注的语义校验，并通过 golden Hash；仅通过 Schema 不代表契约完整。

发布首个稳定版本后，同一 Schema ID 的内容不可变。
