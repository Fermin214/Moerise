# Moerise AI 洞察

AI 洞察围绕选定的个人笔记展开多轮流式对话，并与向量模型（Embedding）完全独立——关闭或更换大语言模型不影响笔记管理、检索与每日回顾。

## 一、功能特性

1. **自由选材与预算控制**：
   - 可在 Web 与 Android 中按日期范围、标签或逐条选择正常与归档笔记作为上下文材料。
   - 单次选材上限为 20,000 个 Unicode 码点、最多 1,000 条笔记。该上限仅统计所选笔记正文，完整请求还包含提示词与历史对话，实际 token 用量由目标模型决定。
2. **流式输出与中断恢复**：
   - 通过 Server-Sent Events（SSE）实时流式返回回复，自动过滤模型输出的 `thinking` / `reasoning` 思维链片段。
   - 会话与消息持久化在 `state.sqlite3`，支持刷新后继续追问、主动停止生成或保存因长度截断的部分回答。
3. **保存回 Memos**：
   - 支持将单条回复或多条选中的问答按对话顺序保存为一篇 Memos 笔记，保留原始 Markdown 格式，不附加额外前缀或标签。

## 二、支持的模型协议

Core 适配以下三种模型协议：

| 协议 (`protocol`) | 请求路径 | 鉴权方式 | 输出上限字段 | 说明 |
| --- | --- | --- | --- | --- |
| `openai-compatible` | `base_url` + `/chat/completions` | `Authorization: Bearer <key>` | `max_completion_tokens` | OpenAI 及兼容 Chat Completions 流式接口的网关 |
| `openai-responses` | `base_url` + `/responses` | `Authorization: Bearer <key>` | `max_output_tokens` | OpenAI Responses API |
| `anthropic` | `base_url` + `/messages` | `x-api-key: <key>`<br>`anthropic-version: 2023-06-01` | `max_tokens` | Anthropic Messages 接口或兼容网关 |

## 三、配置说明

可在「设置 → AI」中管理，或在配置文件的 `llm` 字段初始化：

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

### 字段说明

| 字段 | 类型 | 默认值 | 说明与范围 |
| --- | --- | --- | --- |
| `enabled` | boolean | `false` | 是否启用大模型；关闭时不影响基础功能 |
| `protocol` | string | `openai-compatible` | `openai-compatible`、`openai-responses` 或 `anthropic` |
| `base_url` | string | 必填 | HTTP(S) 基地址，不含末尾斜杠、查询参数或账号密码 |
| `api_key` | string | 必填 | API 密钥，持久化后由服务端加密存储 |
| `model` | string | 必填 | 模型名或路由标识（如 `gpt-4o`、`claude-3-5-sonnet-20241022`） |
| `timeout_seconds` | number | `90` | 单次请求总超时（1 至 110 秒，含重试） |
| `max_concurrency` | integer | `2` | 并发请求数上限（1 至 8） |
| `max_retries` | integer | `2` | 临时网络或服务错误重试次数（0 至 3） |
| `max_output_tokens` | integer | `2048` | 单次生成最大输出 token 数（32 至 16384） |

> [!TIP]
> 环境变量使用 `MOERISE_LLM_*` 前缀（如 `MOERISE_LLM_BASE_URL`、`MOERISE_LLM_API_KEY`），仅用于首次初始化；在设置页保存后以持久设置为准。

## 四、连接诊断

### 1. 设置页检查
在 Web 或 Android 的「设置 → AI」点击 LLM 区域的「检查连接」，Core 会发送一条内置测试消息验证流式响应并返回耗时。

### 2. 命令行检查

独立于数据库与 Memos 执行连通性探测：

```bash
# 二进制环境
./build/moerise check-llm --config .local/config.json

# Docker Compose 环境
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

## 五、上下文管理与会话状态

1. **材料预算**：所选笔记正文超过 20,000 Unicode 码点时返回 `CHAT_MATERIAL_LIMIT`，超过 1,000 条返回 `CHAT_RESOURCE_LIMIT`。
2. **语言偏好**：请求携带界面语言（`zh-CN` 或 `en`）。英文模式下内置洞察提示词切换为英文并附加英文回复要求；自定义 Prompt 与笔记原文保持原语言。
3. **持久化与状态**：
   - 生成启动前基于本地索引解析材料并核对版本号；流式推送期间隔离运行期设置热更新，修改设置不会打断进行中的回复。
   - 达到输出上限停止且已有正文时标记为 `truncated`；服务重启将未完成生成标记为 `interrupted`，两者均保留已落盘文本且不自动续调模型。
   - 修改会话材料后，后续提问使用新版本材料，历史回复保留原材料版本与来源摘要。
   - 「保存为笔记」支持选中提问与已完成回答；`canceled` 或 `truncated` 的部分回答需确认后保存。正文上限 65,536 UTF-8 字节，保存按操作键幂等去重；删除会话不影响已存入 Memos 的笔记。
