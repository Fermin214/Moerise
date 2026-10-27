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
