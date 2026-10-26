# Moerise 向量模型

Moerise 通过独立的提供商接口生成文本向量。百炼适配使用 DashScope 原生文本向量协议，区分文档和查询，并输出连续向量。后台索引将正常与归档笔记分块，分批生成向量并持久保存构建进度。检索和发现的交付安排见[路线图](ROADMAP.md#阶段-3语义检索与发现)。

## 百炼配置

使用阿里云百炼北京地域的 `qwen3.7-text-embedding`，默认 1024 维。API Key 与调用地址须属于对应业务空间和地域；在百炼控制台获取业务空间 ID 和该空间的 API Host。

在服务配置中填写 `embedding`：

```json
{
  "embedding": {
    "enabled": true,
    "provider": "bailian",
    "protocol": "dashscope",
    "base_url": "https://replace-with-your-workspace-id.cn-beijing.maas.aliyuncs.com/api/v1",
    "api_key": "replace-with-a-beijing-bailian-api-key",
    "model": "qwen3.7-text-embedding",
    "dimensions": 1024,
    "batch_size": 20,
    "timeout_seconds": 30,
    "max_concurrency": 2,
    "max_retries": 2,
    "query_instruction": null
  }
}
```

`base_url` 是包含 `/api/v1` 的 DashScope 基地址，例如北京公共端点 `https://dashscope.aliyuncs.com/api/v1` 或业务空间专属地址。适配器添加 `/services/embeddings/text-embedding/text-embedding`。OpenAI 兼容接口的 `/compatible-mode/v1` 地址不用于此适配器。配置值直接从 JSON 读取，不进行环境变量替换。

| 字段 | 含义与范围 |
| --- | --- |
| `enabled` | 默认 `false`；启用时校验模型配置，关闭时基础记录与回顾照常运行 |
| `provider`、`protocol` | `bailian`、`dashscope` |
| `base_url`、`api_key` | DashScope HTTP(S) 基地址与该业务空间的 API Key；地址不含账号密码、查询参数或片段 |
| `model` | `qwen3.7-text-embedding` |
| `dimensions` | 默认 1024；模型支持 256、512、768、1024、1536、2048、2560 |
| `batch_size` | 每个 HTTP 请求的文本数量，默认 20，范围 1 至 20 |
| `timeout_seconds` | 一次业务调用的总超时，包含并发等待、分批请求和重试；默认 30 秒，范围 1 至 120 秒 |
| `max_concurrency` | 一个提供商客户端同时处理的批次数量，默认 2，范围 1 至 8 |
| `max_retries` | 每批首次请求之后的重试次数，默认 2，范围 0 至 3 |
| `query_instruction` | 可选查询任务说明，仅在查询请求中发送；最多 4096 个 UTF-8 字节 |

供应商与模型支持范围的依据见[百炼文本向量接口](https://help.aliyun.com/zh/model-studio/text-embedding-synchronous-api)。其他供应商及通用协议适配见路线图的[后续方向](ROADMAP.md#后续方向与开发输入)。

## 连接检查

原生二进制：

```bash
./build/moerise check-embedding --config .local/config.json
```

容器部署：

```bash
docker compose exec -T moerise moerise check-embedding
```

命令使用两个内置虚构文档和一个示例查询，独立于 Memos 与数据库运行。成功时输出 JSON，包含提供商、协议、模型、维度、文档与查询向量数量、总 Token 用量和耗时；退出码为 0。失败时返回非零退出码和错误，不输出 API Key、文本或向量数值。检查会产生少量模型用量。

`check-config` 只校验本地配置结构，`check-embedding` 通过实际模型请求检查连接和返回格式。检索效果使用路线图 3.6 的真实问题验证。

## 调用与返回

业务服务通过 `EmbeddingProvider` 提交文档或查询文本，不直接处理 DashScope 请求结构。每次调用接受 1 至 1000 条非空 UTF-8 文本，每条最多 65536 字节；超过批次大小时顺序分批，输入不截断。查询按 `text_type=query` 发送，入库文档按 `text_type=document` 发送；两者使用同一模型和维度。

适配器显式请求 `output_type=dense` 和 `dimension`，按返回的 `text_index` 恢复输入顺序，将向量转为 float32，并汇总 Token 用量。返回数量、位置、维度、数值或用量不符合契约时整次调用失败；不返回部分向量。向量保留供应商数值，不额外归一化。

模型连接使用标准 HTTP 代理环境设置，拒绝自动跟随重定向。Memos 连接仍直接访问来源，两者的配置与传输分开。

## 故障处理

| 错误 | 处理方法 |
| --- | --- |
| `EMBEDDING_DISABLED` | 将 `embedding.enabled` 设为 `true` 并填写模型配置 |
| `EMBEDDING_AUTH` | 检查 API Key、业务空间、地域与模型访问权限；不自动重试 |
| `EMBEDDING_REJECTED` | 检查接口地址、模型及请求参数；不自动重试 |
| `EMBEDDING_RATE_LIMITED` | 429 限流；按 `Retry-After` 与指数等待进行有限重试 |
| `EMBEDDING_UNAVAILABLE` | 网络、响应读取或 5xx 故障；进行有限重试 |
| `EMBEDDING_PROTOCOL` | 返回数量、位置、维度、数值或用量错误；不自动重试 |

等待与请求受同一次调用的总超时限制。取消或达到超时后结束调用；供应商错误正文不进入错误输出。

## 分块与向量索引

启用 `embedding` 后，服务在完整同步成功后自动构建向量索引。索引使用独立工作循环，每次处理至多 `batch_size` 个片段，单批向量成功后写入 `index.sqlite3`；模型故障按 `sync.retry_seconds` 间隔重试，不阻塞记录、同步或每日回顾。发送新文档向量请求前检查来源身份，逐条复查涉及的笔记；来源变化先修正投影，再重新规划索引。

分块版本为 `paragraph-v1-2048`。正文不超过 2048 个 Unicode 码点时整条处理；长正文在上限内优先按空行分隔的段落组合，支持 LF、CRLF 和仅含空格或制表符的空行。超长段落优先按换行切分，单行超过上限时按码点切分；不截断正文、不拆开 UTF-8 字符。全空白片段不生成向量，空白笔记仍保留笔记元数据。附件字节不进入文档向量。

每个片段保存笔记资源名、顺序、原始正文的 UTF-8 字节起止位置、起止行号、文本和 SHA-256 摘要。字节位置从 0 开始，终点不包含在区间中；行号从 1 开始，末尾换行属于其前一行。笔记另存来源摘要和正文摘要。向量以小端 float32 连续字节保存，保留供应商数值。

每个索引代次保存提供商、协议、调用地址、模型、维度、分块版本和投影修订号。向量空间摘要由前六项共同确定，API Key、批次大小、重试参数和查询任务说明不改变文档向量空间。修改模型空间或分块方式后建立新代次，旧代次在替换完成前仍保留；只有全部片段具备有效向量，且投影修订号一致时才原子切换。

投影更新或删除会触发新代次，同一空间内按片段正文摘要复用向量。归档、置顶、时间及附件元数据变化不重新计算正文向量；正文变化只计算变化后的片段。构建期间发生投影变化时重新规划，并复用已完成批次。重启后从持久片段进度继续，失败或中断的批次重新请求，已提交的批次不重复调用。发布完成后清理被替换的代次及无引用向量。

通过具备 `sync:read` 的凭据读取 `GET /api/ext/v1/sync`，查看 `vector_index.active`、`building`、`ready` 和 `completed_chunks`。`ready` 仅描述索引与配置及投影的一致性；来源状态单独看 `source_ok`。

分块、向量、缓存和构建进度均为派生数据，不进入产品状态备份。停止服务后执行 `rebuild-index` 可清空并重新生成索引；创建幂等记录、回顾历史、阅读与通知状态保留在 `state.sqlite3`。恢复产品状态快照后重新同步并构建向量。命令见[运行与恢复](OPERATIONS.md#重建索引)。
