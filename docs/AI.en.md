# Moerise AI

[简体中文](AI.md)

Moerise AI supports reflection, multi-turn discussion, and brainstorming around personal notes. Independent protocol adapters support common LLM services. LLM configuration is separate from Embedding; changing or disabling the LLM leaves search, reviews, and basic notes available.

---

## 1. Features & Design

1. **Thinking from your notes**:
   - Explore connections, hypotheses, contradictions, and new ideas from explicitly selected materials.
   - Replies distinguish original note content from AI associations and hypotheses.
2. **Material control**:
   - Select normal or archived notes in Web.
   - Selected note bodies are limited to 20,000 Unicode code points before generation. This limit covers material text; the full request also includes prompts and successful history, with token use determined by the model.
3. **Streaming and persistence**:
   - Server-Sent Events (SSE) deliver incremental output.
   - Messages are stored in `state.sqlite3` so conversations remain available after refresh.
   - Stop generation at any time; saved partial text remains in the conversation.
4. **Save to Memos**:
   - Save a completed assistant reply as a note.
   - Preserve Markdown, code blocks, and newlines without added prefixes or tags.

---

## 2. Model Protocols

The `LLMProvider` interface supports:

| Protocol | Request Path | Authentication | Output Limit | Compatibility |
| --- | --- | --- | --- | --- |
| `openai-compatible` | `base_url` + `/chat/completions` | `Authorization: Bearer <key>` | `max_completion_tokens` | OpenAI or gateways supporting this field and streaming protocol |
| `openai-responses` | `base_url` + `/responses` | `Authorization: Bearer <key>` | `max_output_tokens` | OpenAI Responses API |
| `anthropic` | `base_url` + `/messages` | `x-api-key: <key>`<br>`anthropic-version: 2023-06-01` | `max_tokens` | Anthropic Messages or compatible gateways |

---

## 3. Configuration

Configure `llm` in JSON or through Web settings:

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

### Fields

| Field | Type | Default | Constraints |
| --- | --- | --- | --- |
| `enabled` | boolean | `false` | Enable the LLM; basic features work when disabled |
| `protocol` | string | `openai-compatible` | `openai-compatible`, `openai-responses`, or `anthropic` |
| `base_url` | string | Required | HTTP(S) API base without query parameters or embedded credentials |
| `api_key` | string | Required | Provider credential stored on the server |
| `model` | string | Required | Model name or gateway route, such as `gpt-4o` |
| `timeout_seconds` | number | `90` | Total request budget including retries, 1–110 seconds |
| `max_concurrency` | integer | `2` | Concurrent requests per model client, 1–8 |
| `max_retries` | integer | `2` | Retry limit for eligible temporary errors, 0–3 |
| `max_output_tokens` | integer | `2048` | Output token limit, 32–16384 |

> [!TIP]
> Environment variables use `MOERISE_LLM_*`, such as `MOERISE_LLM_BASE_URL` and `MOERISE_LLM_API_KEY`. Saved product settings take precedence after initialization.

---

## 4. Connection Checks

### 1. Web

Under **Settings → Models & Index**, choose the LLM connection check. Core sends a built-in test message and validates a streaming completion, returning availability and latency.

### 2. CLI

Checks run independently of Memos and product databases:

```bash
./build/moerise check-llm --config .local/config.json
```

In Docker:

```bash
docker compose exec -T moerise moerise check-llm
```

Example output:

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

## 5. Context & Conversation State

1. **Material limits**:
   - More than 20,000 Unicode code points returns `CHAT_MATERIAL_LIMIT` before generation.
   - Selecting more than 1,000 notes returns `CHAT_RESOURCE_LIMIT`.
2. **Reply language**:
   - Generation freezes the interface language, `zh-CN` or `en`, with the request.
   - English requests add an English reply instruction. The unchanged built-in insight prompt uses its English equivalent; custom prompts and materials remain verbatim.
3. **Persistent conversations and saving**:
   - Sessions, turns, and material references are stored in `state.sqlite3`.
   - Restart marks unfinished generation `interrupted`, preserving saved text without another automatic model call. Users can continue with a follow-up.
   - Saving writes the selected assistant reply unchanged and records an idempotent association to prevent duplicate notes.
