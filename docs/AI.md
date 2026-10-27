# Moerise AI

LLM 通过独立的协议适配器生成内容，支持 OpenAI Chat Completions、OpenAI Responses 和 Anthropic Messages，与向量模型分别配置。更换 LLM 无需重建向量索引。关闭 LLM 时，记录、回顾、推送与检索继续使用原有业务服务。

## 模型配置

在服务配置中填写 `llm`：

```json
{
  "llm": {
    "enabled": true,
    "protocol": "openai-compatible",
    "base_url": "https://gateway.example.com/v1",
    "api_key": "replace-with-your-model-service-key",
    "model": "replace-with-your-model-or-route",
    "timeout_seconds": 90,
    "max_concurrency": 2,
    "max_retries": 2,
    "max_output_tokens": 2048,
    "context_bytes": 24000
  }
}
```

地址可包含供应商或网关路径，不含账号密码、查询参数或片段。`model` 去除首尾空白后传递给服务，可使用供应商模型名或网关路由名。协议决定请求路径、认证头、输出限额字段和流式事件，业务层使用相同的 `LLMProvider`。

| `protocol` | 请求路径 | 认证与输出上限 |
| --- | --- | --- |
| `openai-compatible` | `base_url` + `/chat/completions` | `Authorization: Bearer`；`max_completion_tokens` |
| `openai-responses` | `base_url` + `/responses` | `Authorization: Bearer`；`max_output_tokens`；`store: false` |
| `anthropic` | `base_url` + `/messages` | `x-api-key`、`anthropic-version: 2023-06-01`；`max_tokens` |

直连 OpenAI 时，基地址可填写 `https://api.openai.com/v1`；直连 Anthropic 时可填写 `https://api.anthropic.com/v1`。兼容网关填写该服务提供的基地址。协议和模型分别配置，不根据模型名猜测协议。

| 字段 | 含义与范围 |
| --- | --- |
| `enabled` | 默认关闭；旧配置未填写此字段时也关闭 |
| `protocol` | `openai-compatible`、`openai-responses` 或 `anthropic`，默认 Chat Completions |
| `base_url`、`api_key` | HTTP(S) 基地址与服务凭据；启用时必填 |
| `model` | 模型或路由名，1 至 200 字节，不含控制字符 |
| `timeout_seconds` | 等待并发、请求、重试和读取响应的总超时；默认 90 秒，范围 1 至 110 秒 |
| `max_concurrency` | 同一模型客户端同时执行的生成数量；默认 2，范围 1 至 8 |
| `max_retries` | 首次请求后的重试次数；默认 2，范围 0 至 3 |
| `max_output_tokens` | 请求的输出上限，按所选协议映射字段；默认 2048，范围 32 至 16384 |
| `context_bytes` | 全部消息正文的 UTF-8 总预算，包含提示词、证据及 JSON 包装；默认 24000，范围 1024 至 262144 |

模型凭据由服务端读取。模型连接采用标准 HTTP 代理环境设置，拒绝自动跟随重定向。来源 Memos 的连接仍使用独立配置。

## 连接检查

原生二进制：

```bash
./build/moerise check-llm --config .local/config.json
```

容器部署：

```bash
docker compose exec -T moerise moerise check-llm
```

命令使用内置虚构文本，实际读取流式响应，独立于 Memos 和数据库运行。成功输出协议、模型、完成原因、用量和耗时，不输出生成正文或凭据。供应商未提供的用量字段为 `null`。失败返回非零退出码与错误代码。

从 Linux 容器连接宿主机网关时，地址应填写容器可访问的宿主机地址，例如 Docker Desktop 的 `http://host.docker.internal:3425/v1`。连接检查与向量模型检查相互独立。

## 调用和故障

`LLMProvider.Generate` 接收角色消息和可选的内容回调。无回调时读取完整 JSON；有回调时读取 SSE，并校验协议对应的结束事件与完成原因。Chat Completions 要求 `stop` 和 `[DONE]`，Responses 要求 `response.completed`，Messages 要求 `end_turn` 和 `message_stop`。业务可以通过取消上下文终止等待、读取和重试。回调返回错误也会立即结束生成。

协议适配只交付文本，工具调用、拒绝、截断和异常终止返回错误。Responses 的推理项与 Messages 的思考块不会作为回答正文；完成原因使用 `stop` 或 Messages 的 `end_turn`。用量保留供应商可提供的输入、输出、总数与缓存输入 Token，未提供的字段为 `null`。

临时限流、服务端故障和网络错误仅在尚未交付内容时有限重试，退避遵守受限的 `Retry-After`。明确的余额耗尽、额度或账单限制不重试。内容回调已收到任一片段后，后续失败不会自动重放。调用方须依据最终完成状态使用内容。

| 错误 | 处理方法 |
| --- | --- |
| `LLM_DISABLED` | 启用模型并填写连接配置 |
| `LLM_AUTH` | 检查模型服务凭据与路由权限 |
| `LLM_REJECTED` | 检查模型对所选协议和请求的支持，以及服务余额与额度 |
| `LLM_RATE_LIMITED` | 降低请求并发，稍后重试 |
| `LLM_UNAVAILABLE` | 检查网关、供应商连接和请求超时 |
| `LLM_PROTOCOL` | 检查返回结构、UTF-8、完成标记和用量字段 |
| `LLM_OUTPUT_LIMIT` | 缩小问题范围或调整输出预算，重新生成 |
| `LLM_CONTEXT_LIMIT` | 缩小证据范围或调整上下文预算 |
| `LLM_TIMEOUT` | 检查供应商延迟，缩小请求或调整总超时 |
| `LLM_CANCELED` | 调用方取消，重新操作时创建新的生成请求 |

服务错误不包含供应商响应正文、输入材料或凭据。无效协议、明确拒绝、取消与回调错误不自动重试。

## 证据与引用

`EvidenceService` 复用混合检索、相关笔记和分块位置收集证据。默认只使用绑定作者的正常笔记，`state` 可显式选择 `ARCHIVED` 或 `ALL`；标签按精确 AND 匹配。`from` 和 `until` 接受 RFC 3339 时间，按笔记创建时间过滤，范围为 `[from, until)`。日期过滤在各召回通道的候选上限及笔记数量上限之前执行，原文复查时也检查同一范围。

| 输入范围 | 证据来源 |
| --- | --- |
| 一条 `names` | 指定笔记的原文片段，以及最多三条符合筛选条件的相关历史；没有相关历史时只保留指定笔记 |
| 两至二十条 `names` | 只使用明确选定的笔记，不补入其他来源 |
| `question`，不指定 `names` | 通过共享混合检索召回；向量不可用时保留关键词和标签结果及语义降级原因 |

每个片段具有服务器分配的 `E1`、`E2` 等 ID，绑定 `name`、`source_hash`、UTF-8 半开字节区间、从 1 开始的行号、原文正文、链接及创建和修改时间。去重以来源、版本和位置为依据；正文相同而来源不同的笔记仍保留各自引用。模型只返回这些 ID，资源名、链接和位置由服务器组装。

证据预算计算整个片段数组序列化后的 JSON 字节数，包含元数据、转义、逗号和括号。生成服务须先为提示词、问题和包装字段预留预算，再传入证据收集器。只允许缩短单条洞察必需的第一个片段，裁切保持 UTF-8、原文字节位置和行号一致；其他装不下的片段跳过。`omitted` 列出因筛选或预算未完整纳入的来源，某条来源可以同时出现在已使用片段与省略列表中。

收集结束、模型调用前、内容输出前及生成结束时均复查来源身份、版本和原文位置。来源改变返回 409 `SOURCE_CHANGED`；删除或作者越界返回 404 `SOURCE_NOT_FOUND`；来源故障终止整个请求。不存在的引用 ID 或不符原文的位置不能进入结果。空检索结果不交给模型补写事实，由业务层返回证据不足。

## 单条笔记洞察

`AIService.Insight` 接受一个笔记资源名和可选的问题、标签、状态及日期条件。原笔记与最多三条相关历史组成上下文；没有相关历史时，只分析指定笔记。结果分为原文事实 `fact`、模型解释 `interpretation` 与追问 `question`，解释不能冒充原文事实。事实与解释必须带证据 ID；关联多条笔记的文字须由涉及各方的引用支撑。

业务结果 `AIResult` 包含 `kind`、`status`、`completed`、`statements`、`references`、`used_sources`、`omitted`、`missing`、`semantic`、`model`、`usage` 和 `metrics`。`status` 为 `answered` 或 `insufficient`，后者说明具体缺少的记录或信息；`completed=true` 只在格式、引用、模型结束信号及最终来源复查均成功后返回。没有可用证据时直接返回不足结果，不调用模型。洞察不会自动保存为笔记。

模型内部使用逐行 JSON 组织内容，业务层按完整条目验证类型、长度和引用，再复查来源并交付 `content` 事件；未完成的 JSON 不会作为正文输出。`references` 事件提供服务器生成的引用元数据，`complete` 事件提供完整业务结果。模型失败、取消、格式错误、虚构引用或来源变化均终止生成；已交付的条目不能视作完整结果，失败请求没有成功完成事件。

输入总预算涵盖提示词、问题、证据元数据与 JSON 转义。模型输出中的笔记指令不产生额外检索或写入操作。指标记录检索耗时、从请求开始到首条有效内容的延迟、生成耗时和总耗时，单位为毫秒；没有生成内容时首条延迟为 `null`。

## 多条笔记洞察

向 `AIService.Insight` 提供两至二十个不同资源名时，结果 `kind=multi`。上下文只包括选定来源，按共同点、差异和联系组织事实与解释。每条关联解释至少引用两个不同资源，不能用同一笔记的两个片段替代双方证据。相同正文保留各自来源，但不能当作独立佐证；说法相反时列明原文差异，不据此推断原因。

`references` 是提供给模型的片段，`used_sources` 是输出条目实际引用的资源名，`omitted` 是未完整纳入的来源。选定来源因筛选或预算完全未进入上下文时，结果使用 `insufficient` 并列明缺少的来源；对其余来源的有据分析仍可使用。部分片段被省略时，通过 `omitted` 标明范围。
