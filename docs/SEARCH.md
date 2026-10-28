# Moerise 搜索

REST 和扩展 MCP 通过同一个 `SearchService` 搜索绑定作者的笔记、查找相关笔记并进行漫步。检索组合中文关键词、标签和可选语义，不依赖 LLM。

## 请求与结果

REST 使用 `POST /api/ext/v1/search`，要求 `notes:read` Scope，成功返回 200。非 POST 方法返回 405 `METHOD_NOT_ALLOWED`；未知 JSON 字段或参数不合法返回 422 `INVALID_ARGUMENT`。

```json
{
  "query": "整理灵感",
  "tags": ["工作/灵感"],
  "state": "NORMAL",
  "limit": 10
}
```

| 字段 | 规则 |
| --- | --- |
| `query` | 必填字符串；trim 后非空，同时满足最多 256 个 Unicode 码点、1024 个 UTF-8 字节 |
| `mode` | 默认 `hybrid`；可选 `keyword`、`semantic` |
| `tags` | 可选字符串数组，默认 `[]`；输入最多 20 项，去重后按字符串升序排序 |
| `state` | 默认 `NORMAL`；接受 `NORMAL`、`ARCHIVED`、`ALL` |
| `limit` | 默认 10；整数，1 至 20 |

标签格式与记录一致：每项 1 至 100 个 Unicode 码点，Unicode 字母、十进制数字、下划线或连字符组成非空段，段间用 `/` 分隔；不含 `#`、空白、空段或上标等其他数字形式。标签按来源标签集合精确匹配，多个标签使用 AND，不作前缀或模糊匹配。作者、状态和标签条件对全部召回通道共同生效。`ALL` 仅包含绑定作者的正常与归档笔记。

`SearchResponse` 包含 trim 后的 `query`、实际执行的 `mode`、有序 `results` 和 `semantic`。无命中时 `results` 为 `[]`。每个 `SearchHit` 包含：

| 字段 | 含义 |
| --- | --- |
| `note` | 来源复查后的完整 `Note`，含资源名、作者、状态、标签、正文和原文 `url` |
| `score` | 融合后该笔记最高片段的数值分数，不能用作概率或置信度 |
| `matches` | 最多 3 个命中片段 |

每个 `SearchMatch` 包含以下字段：

| 字段 | 类型与位置约定 |
| --- | --- |
| `ordinal` | 整数，从 0 开始的笔记片段序号 |
| `byte_start`、`byte_end` | 整数，原始正文 UTF-8 字节位置，从 0 开始的半开区间 `[byte_start, byte_end)` |
| `line_start`、`line_end` | 整数，原始正文起止行号，从 1 开始 |
| `content` | 字符串，对应原始正文区间的片段 |
| `signals` | 字符串数组，取值为 `keyword`、`semantic`、`tag`，表示参与命中的召回通道 |

片段使用 `paragraph-v1-2048` 分块：短正文整条处理，长正文按段落、换行和码点切分，不拆开 UTF-8 字符；末尾换行属于前一行。完整规则见[分块与向量索引](EMBEDDING.md#分块与向量索引)。命中位置以原始正文为准，分词不改变位置。

## 中文关键词与排名

关键词索引使用预分词 FTS5。连续中文文本生成单字和相邻双字，英文与数字按整词转小写。分块交界处的相邻中文双字同时记入两侧片段，避免短查询跨块漏召回，片段正文与来源位置不变。查询中长度至少为 2 的连续中文使用相邻双字召回；中文单字走专门召回路径，覆盖一字、两字等短查询。服务对生成的词项安全引用后构造 FTS 表达式，不将原查询直接用作 SQL 或 FTS 语法。

三个通道分别召回片段：

1. 关键词通道按 BM25 取前 100 个片段。
2. 标签匹配通道取前 100 个片段。
3. 语义通道使用 float32 向量精确计算余弦相似度，仅保留正余弦结果，取前 100 个片段。

先在片段层按 RRF 融合，参数 k=60，每个通道从第 1 名开始贡献 `1 / (60 + rank)`。同一片段合并命中信号，再按笔记合并；笔记分数取最高片段分数，每条最多返回 3 个片段，最终受 `limit` 限制。分数降序排列，同分按资源名、片段序号升序排列；标签通道按资源名、片段序号升序排列。

## 语义缓存与降级

每个服务复用一个活动代次的内存 float32 向量缓存，查询不重复载入整份矩阵。缓存仅在向量空间与配置、投影修订号一致时可用，新代次完整发布后再使用。文档和查询使用同一向量空间，模型配置与构建流程见[向量模型](EMBEDDING.md)。

响应中的 `semantic` 始终包含 `used: bool` 和 `reason: string`：

| 条件 | used | reason |
| --- | --- | --- |
| 使用语义通道 | `true` | `""` |
| 显式 `keyword` 模式 | `false` | `KEYWORD_MODE` |
| 关闭 embedding | `false` | `EMBEDDING_DISABLED` |
| 活动索引未就绪或与配置、投影修订不一致 | `false` | `INDEX_NOT_READY` |
| 查询模型调用失败 | `false` | 具体模型错误代码，例如 `EMBEDDING_AUTH`、`EMBEDDING_RATE_LIMITED`、`EMBEDDING_UNAVAILABLE`、`EMBEDDING_PROTOCOL` |

`hybrid` 为默认模式，使用全部通道，语义不可用时返回 `mode=keyword` 及明确原因；`keyword` 只使用关键词和标签，不调用模型；`semantic` 只使用语义通道，模型关闭、索引未就绪或查询失败返回 503 和对应原因，不返回关键词结果。三种模式共用来源复查、状态和标签过滤。

混合模式下关闭模型、索引未就绪或模型失败时，关键词与标签通道继续工作，成功响应显式说明语义状态。请求取消直接结束，不转为关键词降级成功。来源故障不属于语义降级。

## 来源复查

1. 搜索前验证 Memos 来源版本和绑定身份。
2. 在统一过滤条件内召回、融合候选片段并合并笔记。
3. 返回前逐条从 Memos 读取候选原文，检查作者、状态、标签和来源摘要。发生修改、删除、作者越界或过滤条件变化的候选丢弃，同时修正投影，后续请求使用修正后的索引。
4. 输出前再次验证来源身份，全部检查成功后才返回结果。

候选丢弃后结果可能少于 `limit`，甚至为空。任一步来源故障使整个请求失败，不返回部分缓存文本。REST 错误沿用 [API v1](API-V1.md#时间与错误) 的来源错误契约；MCP 与飞书发送队列中的搜索回复执行相同来源检查。

## 相关笔记

REST 使用 `GET /api/ext/v1/notes/{memo_id}/related`，要求 `notes:read`。查询参数 `tags` 通过重复字段传入，最多 20 项，格式及精确 AND 规则同搜索；`state` 默认 `NORMAL`，可选 `ARCHIVED`、`ALL`；`limit` 默认 10，范围 1 至 20。未知参数、重复 `state`/`limit`、非法资源名或参数返回 422，非 GET 返回 405。原笔记不受候选状态或显式标签过滤，归档原笔记可以召回正常候选。

响应 `RelatedResponse` 为 `source: Note`、`results: SearchHit[]`、`semantic: SearchSemantic`。无结果返回 `[]`；命中字段和来源位置复用搜索约定。

### 召回与融合

- 关键词从全量原文提取，中文单字、双字及英文数字规则同上，不受短查询的 256 码点限制。词项编码前的 UTF-8 长度超过 128 字节时跳过；按出现频次降序、词项升序选择最多 64 个，安全引用后执行 BM25。
- 标签通道以原笔记标签进行精确共同标签匹配，按共同标签数量降序、资源名和片段序号升序排列，不自动要求候选具备全部原笔记标签。请求显式 `tags` 作为独立的 AND 过滤，对所有通道生效。
- 语义通道复用同一活动代次的文档向量，不调用查询模型、LLM 或额外 embedding。原笔记不同正文片段的向量分别 L2 归一化，再等权平均并归一化；相同正文片段只计一次。按候选片段的正余弦相似度排序。

每通道先排除原笔记，再取前 100 片段；RRF k=60 先融合片段再按笔记合并，笔记取最高片段分。每笔记最多 3 个不同正文片段；相同正文合并 `signals`，保留最高分片段的位置。其余稳定排序规则见[中文关键词与排名](#中文关键词与排名)。

语义状态复用 `SearchSemantic`：启用且可用时为 `used=true, reason=""`；关闭 embedding 为 `EMBEDDING_DISABLED`，索引空间或投影修订不匹配、活动索引未就绪为 `INDEX_NOT_READY`，原笔记无可用片段向量为 `NO_SOURCE_VECTORS`。降级后继续关键词与共同标签召回；取消不降级。

### 来源一致性

先复查来源版本与身份，再读取原笔记、修正其投影并召回候选。候选逐条原文复查与搜索一致，变化候选丢弃并修正投影。输出前再次读取原笔记、验证来源摘要与身份；原笔记消失或作者越界返回 404 `SOURCE_NOT_FOUND`，处理中改变返回 409 `SOURCE_CHANGED` 且 `retryable=true`。任何来源故障使整个请求失败，不返回原笔记或部分结果。

相关笔记使用共享分块、关键词索引与活动向量缓存，查询不写入产品状态。

## 语义漫步

`POST /api/ext/v1/discovery/walk` 使用 `notes:read`，接受以下字段；未知字段或非法参数返回 422：

```json
{"name":"memos/start","mode":"angle","path":["memos/previous","memos/start"],"include_archived":false}
```

| 字段 | 规则 |
| --- | --- |
| `name` | 起点资源名；`continue`、`angle` 必填，`random` 可省略 |
| `mode` | 默认 `continue`；可选 `angle`、`random` |
| `path` | 已访问资源名数组，输入至多 100 项；去重并排除起点及路径资源 |
| `include_archived` | 默认 `false`；为 `true` 时允许归档候选 |

`continue` 和 `angle` 复用相关笔记的文档向量聚合及正余弦候选，每次最多考察前 100 个片段。`continue` 对共同顶层标签加 0.2；`angle` 对共同顶层标签减 0.3，对其他主题加 0.15。无共同标签时按语义相关度选择。无需查询模型或 LLM；模型关闭、索引未就绪或起点无向量分别返回 503 `EMBEDDING_DISABLED`、`INDEX_NOT_READY`、`NO_SOURCE_VECTORS`。

`random` 从排除路径后的投影抽样至多 100 条，无需模型，基础分数为 0.5。最近七天通过发现打开接口或回顾显式打开的资源减 0.4；展示、召回、推送或漫步本身不记录阅读。同分随机候选保持抽样顺序。

成功返回 `WalkResponse`：`mode`、一个 `item: SearchHit | null`、后续 `path`、`exhausted` 和 `warnings`。调用方应原样传回路径。达到 100 项或候选范围用尽时返回 `item=null`、`exhausted=true`；该状态只描述路径或候选范围，范围之外仍可能有未读笔记。漫步的 `score` 为语义或随机基础分与话题、阅读权重之和，片段 `signals` 还可包含 `random`、`recent_read`。

来源复查规则与相关笔记一致：开始和结束验证身份，逐条读取候选；起点输出前再次复查，改变返回可重试的 409 `SOURCE_CHANGED`，消失或作者越界返回 404 `SOURCE_NOT_FOUND`。来源故障使整请求失败。

`POST /api/ext/v1/discovery/open` 接受 `{"name":"memos/id"}`，要求 `discovery:write`。成功复查来源后返回 `Note`，更新产品数据库 `discovery_reads.opened_at`。显式打开与回顾打开共同参与最近阅读降权，点击 Memos 链接不自动登记。记录仅含绑定、资源名和时间，随产品状态备份；索引重建保留阅读记录。

`GET /api/ext/v1/search/index` 使用 `notes:read`，返回与同步状态 `vector_index` 相同的 `projection_revision`、`ready`、`active`、`building`。模型关闭时 `ready=false`，不用申请同步管理 Scope 即可检查检索索引。

## MCP 与飞书

扩展 MCP 工具为 `semantic_search`，仅对具备 `notes:read` 的凭据可见及可调用。输入字段与 `SearchRequest`、结构化输出与 `SearchResponse` 一致；设置 `readOnlyHint=true`、`idempotentHint=true`、`destructiveHint=false`。工具语义为只读、幂等、非破坏，来源复查修正的仅是派生索引。

飞书文字全部原样记录为笔记，`搜索 查询词`、`搜索 #标签 查询词` 和 `搜索 #标签` 也按正文保存，不执行搜索。记录与事件恢复见[飞书入口](FEISHU.md)。

飞书发送队列中的搜索回复使用事件结果引用里加密保存的查询，发送时重新搜索并复查来源，展示片段、行位置、资源名和原文 URL；语义降级附简短提示。结果引用不保存用于直接发送的搜索正文，恢复发送时使用配套应用密钥解密查询。

`get_related_memos` 要求 `notes:read`，使用 `readOnlyHint=true`、`idempotentHint=true`、`destructiveHint=false`。输入为 `RelatedRequest`：必填 `name: "memos/id"`，可选 `tags`、`state`、`limit`，默认值和约束同相关 REST；输入错误以 `isError=true` 和错误文本返回，业务错误沿用其他工具的 JSON 错误形状。结构化输出为 `RelatedResponse`。

飞书中的 `相关 memos/资源标识` 按正文保存，不执行相关查询。发送队列中的相关笔记回复默认取 5 条、`NORMAL`，发送时重新获取相关结果，复查原笔记及候选；展示原笔记资源名和 URL、结果片段、行号、资源名与 URL，以及语义降级原因。来源故障不发送原笔记或部分结果。

MCP `semantic_walk` 使用 `WalkRequest`，要求 `notes:read`，工具提示为只读、幂等、非破坏；调用不改变产品状态，随机模式的结果可变化。外部 MCP 不注册 `open_discovery_memo`，阅读记录通过要求 `discovery:write` 的 REST 接口更新，参数为 `name`。飞书中的“漫步”“继续这个话题”“换个角度”“随机跳转”和“查看”全部按正文记录，不执行漫步或更新阅读状态，见[飞书入口](FEISHU.md#私聊记录)。

## 索引与恢复

片段与关键词索引独立于 embedding，关闭模型也可构建和搜索。服务启动时补齐投影对应的片段和关键词；投影修改、删除或来源复查修正后，同步维护派生索引。

片段、FTS5、向量缓存和索引代次保存在可重建的索引范围，不进入 `state.sqlite3` 产品状态备份。重建索引和恢复产品状态后，从 Memos 重新同步并构建；记录幂等、回顾、飞书事件及发送状态继续由产品状态保存。操作见[运行与恢复](OPERATIONS.md)。

## 验证

基础验证包括 `go test ./...`、`go vet ./...`、`gofmt -l main.go internal tests` 和支持竞争检测环境中的 `go test -race ./...`。真实 Memos 隔离集成通过 `go test -tags=integration ./tests -v -count=1` 执行，结合模型与飞书模拟服务覆盖中文短查询、语义改写、标签、跨入口结果、来源修改删除、故障和重启。检查项目与环境见[开发指南](DEVELOPMENT.md)。

实模型评测与本地性能测量的入口为 `TestStage3LiveAcceptance` 和 `scripts/benchmark-search.py`，配置及执行方式见[开发指南](DEVELOPMENT.md#检索评测)。
