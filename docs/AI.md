# Moerise AI

LLM 通过独立的协议适配器生成内容，支持 OpenAI Chat Completions、OpenAI Responses 和 Anthropic Messages，与向量模型分别配置。更换 LLM 无需重建向量索引。关闭 LLM 时，记录、回顾、推送与检索照常运行。

## 模型通信与接口范围

模型配置、连接检查、消息数组和流式输出由共享服务提供。机器 REST 的 `/api/ext/v1/insights`、`/api/ext/v1/answers` 在 Bearer 验证后返回 403 `FORBIDDEN`；`ai:generate` 不能开放生成。MCP 只注册读取工具，不注册 `analyze_memos`、`ask_memos`、`get_topic_timeline`，直接调用这些名称返回未知工具。

旧结构化洞察、问答和主题时间线生成算法已移除。App 直接持有通用 `LLMProvider`，供自然聊天与连接检查共用；历史结果解码、证据复查和队列恢复继续保留，不改写已应用迁移或删除待发记录。机器读取协议见 [API v1](API-V1.md)。

旧机器生成 Handler 和飞书新生成编排已移除，公开 OpenAPI 不再包含其孤立请求、结果或流事件 Schema。飞书仅识别并恢复已有生成记录，不解析文字来创建 AI 任务。旧失败/中断通知不再提示重新发送 AI 命令，历史回顾回复也不再提示换组、打开或略过。

## 自然聊天共享服务

AI 洞察用于从笔记出发自由联想、头脑风暴和深入讨论，鼓励跨领域类比、大胆假设、多种解释与新的尝试。想法可以超出原文，结合常识和想象展开；自然交代哪些是笔记原意、哪些是新想法即可，不要求每个联想都有原文证明，也不使用固定证据分类或逐项引用模板。

`ChatService` 与 App 共享 Notes、Records、Store 和唯一的有效 LLM Provider，不调用 Embedding 或结构化证据编排。创建会话保存明确选定的资源名和来源版本，每次生成从官方 Memos API 读取全部原文，包括归档笔记和同文但不同资源的笔记。不会再按日期、标签筛掉材料，也不会补入相关历史。删除、作者越界或来源变化分别报告 `SOURCE_NOT_FOUND` 或 `SOURCE_CHANGED`；用户可明确重新选择材料并更新会话，旧消息和每轮的材料引用不改写。

“一键洞察”使用已保存的 `insights.default_prompt`，自由提问保留原问题；默认 Prompt 的编辑与恢复使用共享设置。模型输入包含系统约定、完整材料 JSON、全部成功对话及用户问题，输出为自然 Markdown。所选笔记正文合计最多 20,000 个 Unicode 码点，恰好达到上限可以生成；超过时在调用模型前返回 `CHAT_MATERIAL_LIMIT`。提示词、材料元数据、用户问题和后续对话不计入此上限，不另设总输入字节数或消息条数门槛，不裁切材料或历史。模型服务自身的上下文容量仍由所选模型决定，调用失败明确反馈。材料资源超过 1,000 条返回独立的 `CHAT_RESOURCE_LIMIT`。纯附件材料只提供附件元数据，不读取图像或文件内容。

生成请求可携带 `language=en` 或 `zh-CN`，省略或空字符串时沿用简体中文。系统提示设置该次回复语言，用户明确要求其他语言时可覆盖。英语请求将未修改的内置洞察 Prompt 替换为英文版本，自定义 Prompt 和笔记材料按原文传递。实际使用的 Prompt 保存在消息中，不改写共享设置。语言参与请求哈希，切换界面语言后的原请求重试仍需保持原语言。

生成先持久化用户与 running 助手消息，文本增量有界缓冲并在输出前落盘，完成后再发布 `complete`。首次正文输出前和最终完成前均复查全部材料。供应商异常、协议缺少完成标记或来源变化使消息为 failed；主动停止为 canceled，连接断开、消费者中断或 Core 重启为 interrupted。部分文本保留供恢复，EOF 不能作为完成标记。同 `request_id` 的重试返回原消息，不重跑模型；失败后需明确提交新的请求 ID。一个会话同时仅有一条生成链路。

生成和追问不写 Memos。主动保存固定已完成助手消息的版本，以稳定幂等键通过 Records 原样创建笔记，保留 Markdown、空白、换行与正文标签。删除会话清理其产品状态，保留已主动保存的 Memos 笔记与写入恢复元数据；进行中的生成须先停止，保存操作与删除排空互斥。会话和材料修改携带读取版本，冲突返回 `CHAT_CONFLICT`。

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
    "max_output_tokens": 2048
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
| `enabled` | 默认关闭；未填写此字段时使用默认值 |
| `protocol` | `openai-compatible`、`openai-responses` 或 `anthropic`，默认 Chat Completions |
| `base_url`、`api_key` | HTTP(S) 基地址与服务凭据；启用时必填 |
| `model` | 模型或路由名，1 至 200 字节，不含控制字符 |
| `timeout_seconds` | 等待并发、请求、重试和读取响应的总超时；默认 90 秒，范围 1 至 110 秒 |
| `max_concurrency` | 同一模型客户端同时执行的生成数量；默认 2，范围 1 至 8 |
| `max_retries` | 首次请求后的重试次数；默认 2，范围 0 至 3 |
| `max_output_tokens` | 请求的输出上限，按所选协议映射字段；默认 2048，范围 32 至 16384 |
| `context_bytes` | 已停用，仅为旧配置和持久设置兼容保留；不限制材料、问题或对话，Web 不再提供此项 |

模型凭据由服务端读取。模型连接采用标准 HTTP 代理环境设置，拒绝自动跟随重定向。来源 Memos 的连接仍使用独立配置。

## 连接检查

Web 设置页可检查已保存的 LLM 配置，使用共享 Provider 和内置虚构文本读取流式回复，显示成功耗时或脱敏错误。检查独立于 Embedding，不新增笔记或旧生成任务；本人会话协议见[产品设置](SETTINGS.md#模型与索引状态)。

原生二进制：

```bash
./build/moerise check-llm --config .local/config.json
```

容器部署：

```bash
docker compose exec -T moerise moerise check-llm
```

命令使用内置虚构文本，实际读取流式响应，独立于 Memos 和数据库运行。成功输出协议、模型、完成原因、用量和耗时，不输出生成正文或凭据。供应商未提供且无法由已报告字段计算的用量字段为 `null`。失败返回非零退出码与错误代码。

从 Linux 容器连接宿主机网关时，地址应填写容器可访问的宿主机地址，例如 Docker Desktop 的 `http://host.docker.internal:3425/v1`。连接检查与向量模型检查相互独立。

## 用量

连接检查和业务结果使用相同的 `LLMUsage`，五个字段均存在，无法取得的值为 `null`，供应商明确报告零时为 `0`。

| 字段 | 含义 |
| --- | --- |
| `input_tokens` | 总输入 token，包含已报告的缓存读取和缓存写入 |
| `output_tokens` | 输出 token |
| `total_tokens` | 输入与输出总量 |
| `cached_tokens` | 缓存读取的输入 token |
| `cache_creation_tokens` | 缓存写入的输入 token |

Anthropic 原生 `input_tokens` 只表示未缓存的输入。适配器将其与已报告的 `cache_read_input_tokens`、`cache_creation_input_tokens` 相加，映射为总输入；缓存读写分别保留到 `cached_tokens`、`cache_creation_tokens`，不能再次加入总量。例如原生输入 11、缓存读 7、缓存写 5、输出 3，归一化后输入为 23、总量为 26；没有缓存字段的输入 25、输出 1，仍为输入 25、总量 26，两个缓存字段为 `null`。

Messages 的 JSON 和 SSE 使用相同换算。流式事件保留原生字段的最新累计值，省略或 `null` 不覆盖先前计数，不对重复快照逐次求和。缺少原生输入时，总输入保持 `null`，已知的缓存读写仍分别返回。供应商明确报告的 `total_tokens` 优先保留；未报告时，输入和输出齐全则按归一化计数计算。非负计数的求和若超出 int64，无法表示的派生汇总返回 `null`，仍保留已报告的总量与可表示的分项。

Chat Completions 和 Responses 沿用供应商报告的总输入与总量；未报告缓存写入时，`cache_creation_tokens=null`。

## 调用和故障

`LLMProvider.Generate` 接收角色消息和可选的内容回调。无回调时读取完整 JSON；有回调时读取 SSE，并校验协议对应的结束事件与完成原因。Chat Completions 要求 `stop` 和 `[DONE]`，Responses 要求 `response.completed`，Messages 要求 `end_turn` 和 `message_stop`。业务可以通过取消上下文终止等待、读取和重试。回调返回错误也会立即结束生成。

协议适配器只返回文本，工具调用、拒绝、截断和异常终止返回错误。Responses 的推理项与 Messages 的思考块不作为回答正文；完成原因使用 `stop` 或 Messages 的 `end_turn`。用量保留供应商可提供的输入、输出、总数与缓存输入 Token，未提供的字段为 `null`。

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
| `LLM_TIMEOUT` | 检查供应商延迟，缩小请求或调整总超时 |
| `LLM_CANCELED` | 调用方取消，重新操作时创建新的生成请求 |

服务错误不包含供应商响应正文、输入材料或凭据。无效协议、明确拒绝、取消与回调错误不自动重试。

## 历史结果与证据复查

`internal/ai_models.go` 保留旧持久结果的解码结构。`AIResult` 包含 `kind`、`status`、`completed`、`statements`、`references`、`used_sources`、`omitted`、`missing`、`timeline`、`semantic`、`model`、`usage` 和 `metrics`；`AIStatement` 保留事实、解释和追问及引用 ID。`AITimelinePeriod` 保留时间段、代表来源、条目索引及空段原因。这些字段用于读取和发送已有结果，不提供重新生成接口。

历史 `Evidence` 以 `E1`、`E2` 等 ID 绑定资源名、`source_hash`、UTF-8 半开字节区间、从 1 开始的行号、原文片段、链接及创建和修改时间。`EvidenceService.Verify` 在每片发送前通过官方 Memos API 核验来源身份、作者、状态、版本及完整位置，拒绝伪造或失效引用。来源变化返回 `SOURCE_CHANGED`，删除或失权返回 `SOURCE_NOT_FOUND`，来源故障终止发送。历史片段不能替代实时原文。

自然聊天使用明确选择的完整材料和每轮材料版本，不执行旧证据收集、自动关联历史、预算裁切、固定事实分类或时间线生成算法。质量评测与历史恢复测试分别见[开发指南](DEVELOPMENT.md#自然聊天质量评测)。

## 历史渠道任务恢复

渠道队列中的生成任务沿用入队时的类型、加密请求及幂等键。完整生成结果先持久保存，再按顺序发送分片；重投或重启复用已保存结果，不再次调用模型。生成中断、引用失效或来源变化按任务状态处理，发送前继续复查来源。

发送回执、结果不明状态及人工确认记录随产品状态备份和恢复。恢复快照后仍需确认结果不明的发送；已确认分片不重复发送。任务恢复通过共享服务和渠道工作循环执行，机器 REST/MCP 的生成及通知操作保持关闭。通知状态与恢复规则见[通知与推送](NOTIFICATIONS.md)。

全部分片已送达或确认送达后，旧渠道任务清空请求、结果和分片正文，保留去重、回执及恢复元数据。该回收随最终通知状态一起提交，也处理升级前已完成的任务；结果不明任务在确认前保留内容。已清理的空字段不再参与队列密钥迁移。Web 聊天消息和用户主动保存的 Memos 笔记不受影响。
