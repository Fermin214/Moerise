# Moerise AI 模块

Moerise AI 为用户提供围绕个人笔记展开深度思考、多轮探讨与创意发散的能力。通过独立的协议适配器，Moerise 支持主流的大语言模型服务，并与向量模型（Embedding）完全解耦——更换或关闭 LLM 不影响笔记检索、每日回顾与基础笔记管理。

---

## 一、功能特性与设计理念

1. **从个人笔记出发的自由思考**：
   - AI 洞察鼓励以选定笔记为基石，进行跨领域联想、假设验证、潜在矛盾梳理以及头脑风暴。
   - 回复中自然辨析笔记原意与 AI 延展想法，既尊重记录事实，又开拓全新视野。
2. **精准的材料上下文控制**：
   - 用户可以在 Web 界面自由选择多篇正常或归档笔记作为当前探讨的上下文材料。
   - 系统统计所选笔记正文的 Unicode 码点总数（默认上限 20,000 码点），在发起调用前进行严格预算检查，防止超出模型上下文或产生高额账单。
3. **流式传输与中断可恢复**：
   - 会话采用 Server-Sent Events (SSE) 协议提供实时的流式 token 输出。
   - 对话消息持久化存储于 `state.sqlite3`，支持刷新页面后无缝继续追问。
   - 用户可随时点击“停止生成”中断输出，断开的未完成内容保留在会话中。
4. **一键成果沉淀到 Memos**：
   - 满意的 AI 产出或总结成果，支持通过客户端一键保存为 Memos 笔记。
   - 保持完整的 Markdown 格式、代码块与换行，不添加多余的前缀或标签。

---

## 二、支持的模型协议

Moerise Core 统一实现了通用的 `LLMProvider`，适配以下三大主流模型通信协议：

| 协议名称 (`protocol`) | 请求路径 | 鉴权格式 | 输出上限参数 | 适用场景 |
| --- | --- | --- | --- | --- |
| `openai-compatible` | `base_url` + `/chat/completions` | `Authorization: Bearer <key>` | `max_completion_tokens` / `max_tokens` | OpenAI 官方及兼容网关（DeepSeek、OneAPI、Ollama 等） |
| `openai-responses` | `base_url` + `/responses` | `Authorization: Bearer <key>` | `max_output_tokens` | OpenAI 最新 Responses API 协议 |
| `anthropic` | `base_url` + `/messages` | `x-api-key: <key>`<br>`anthropic-version: 2023-06-01` | `max_tokens` | Anthropic Claude 官方接口或中转服务 |

---

## 三、配置说明

在配置文件中设置 `llm` 对象，或通过 Web 设置页面进行可视化管理：

```json
{
  "llm": {
    "enabled": true,
    "protocol": "openai-compatible",
    "base_url": "https://api.openai.com/v1",
    "api_key": "sk-proj-xxxxxxxxxxxxxxxxxxxxxxxx",
    "model": "gpt-4o",
    "timeout_seconds": 90,
    "max_concurrency": 2,
    "max_retries": 2,
    "max_output_tokens": 2048
  }
}
```

### 字段说明与约束

| 字段 | 类型 | 默认值 | 约束与说明 |
| --- | --- | --- | --- |
| `enabled` | boolean | `false` | 是否启用大模型功能；未配置或关闭时基础功能照常运行 |
| `protocol` | string | `openai-compatible` | 可选 `openai-compatible`、`openai-responses` 或 `anthropic` |
| `base_url` | string | 必填 | 服务 HTTP(S) 基地址，不含末尾斜杠、查询参数或账号密码 |
| `api_key` | string | 必填 | 模型服务的 API 密钥；在服务端妥善加密保管 |
| `model` | string | 必填 | 具体的模型名称或网关路由标识（如 `gpt-4o`, `claude-3-5-sonnet-20241022`） |
| `timeout_seconds` | integer | `90` | 单次请求总超时时长（范围 1 至 110 秒），包含重试时间 |
| `max_concurrency` | integer | `2` | 允许同一模型客户端同时发起的请求并发数（范围 1 至 8） |
| `max_retries` | integer | `2` | 网络抖动或模型临时不可用时的最大重试次数（范围 0 至 3） |
| `max_output_tokens` | integer | `2048` | 单次生成允许的最大 token 数量（范围 32 至 16384） |

> [!TIP]
> 环境变量配置时使用 `MOERISE_LLM_*` 前缀（例如 `MOERISE_LLM_BASE_URL`、`MOERISE_LLM_API_KEY` 等）。若已在 Web 界面保存过持久化设置，则以持久化配置为准。

---

## 四、连接诊断与健康检查

### 1. Web 界面诊断
在 Web 界面的「设置」→「模型与索引」中，点击 LLM 区域的「检查连接」按钮。Core 将向大模型发送一条内置测试消息并验证流式响应，直观显示端到端延迟与可用状态。

### 2. 命令行诊断

在服务端可直接使用 CLI 命令进行模型连通性检测，该命令独立于数据库和 Memos 运行：

**二进制环境：**
```bash
./build/moerise check-llm --config .local/config.json
```

**Docker 容器环境：**
```bash
docker compose exec -T moerise moerise check-llm
```

成功输出示例：
```json
{
  "status": "ok",
  "protocol": "openai-compatible",
  "model": "gpt-4o",
  "finish_reason": "stop",
  "duration_ms": 1150,
  "usage": {
    "input_tokens": 32,
    "output_tokens": 12,
    "total_tokens": 44,
    "cached_tokens": 0
  }
}
```

---

## 五、上下文管理与会话状态

1. **材料预算限制**：
   - 圈选笔记正文总字符数若超过 20,000 Unicode 码点，系统将在发起调用前拦截并返回 `CHAT_MATERIAL_LIMIT` 错误提示，指导用户精简材料。
   - 圈选笔记条数上限为 1,000 条（返回 `CHAT_RESOURCE_LIMIT`）。
2. **多语言回复约定**：
   - 生成请求默认携带界面当前语言偏好（`language=zh-CN` 或 `en`）。
   - 若用户未修改内置 Prompt，英文环境将自动切换为地道的英文系统 Prompt；用户自定义的 Prompt 和笔记材料始终保留原语言内容不作篡改。
3. **会话持久化与幂等保存**：
   - 会话记录、消息序列与引用的材料版本保存在 `state.sqlite3` 中。
   - 点击“保存为笔记”时，系统将选定的助手回答以原样纯文本形式写入 Memos，并记录幂等映射，防止因连击产生重复笔记。
