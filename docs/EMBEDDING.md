# Moerise 向量模型 (Embedding)

Moerise 将笔记正文分块后调用向量模型生成嵌入向量，供语义检索、混合检索、随机漫步和观星使用。向量模型与大语言模型（LLM）独立配置、分别启停。观星直接复用已生成的聚合笔记向量，不额外调用 Embedding 模型。

## 一、支持的模型协议

1. **OpenAI 兼容协议 (`openai-compatible`)**：适配 OpenAI `text-embedding-3-*` 以及 Ollama、vLLM 等兼容接口。
2. **阿里云百炼 DashScope 原生协议**：`provider=bailian`、`protocol=dashscope`，适配 `qwen3.7-text-embedding` 的文档与查询向量。

## 二、配置示例

可在「设置 → AI」配置，或在配置文件的 `embedding` 字段初始化：

### 1. OpenAI 兼容协议

```json
{
  "embedding": {
    "enabled": true,
    "provider": "openai-compatible",
    "protocol": "openai-compatible",
    "base_url": "https://api.openai.com/v1",
    "api_key": "sk-proj-xxxxxxxxxxxxxxxxxxxxxxxx",
    "model": "text-embedding-3-small",
    "dimensions": 1536,
    "send_dimensions": false,
    "batch_size": 20,
    "timeout_seconds": 30,
    "max_concurrency": 2,
    "max_retries": 2
  }
}
```

- `dimensions`：模型实际输出的向量维度（1 至 16384），必须与返回值一致。
- `send_dimensions`：支持请求体显式指定维度的模型（如 `text-embedding-3-*`）可设为 `true`；固定维度模型保持 `false`。

### 2. 阿里云百炼

```json
{
  "embedding": {
    "enabled": true,
    "provider": "bailian",
    "protocol": "dashscope",
    "base_url": "https://dashscope.aliyuncs.com/api/v1",
    "api_key": "sk-xxxxxxxxxxxxxxxxxxxxxxxx",
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

- `base_url`：包含 `/api/v1` 的 DashScope 地址，适配器会自动拼接 `/services/embeddings/text-embedding/text-embedding`。
- `dimensions`：`qwen3.7-text-embedding` 支持 256、512、768、1024、1536、2048、2560 维（默认 1024）。

## 三、分块策略与向量索引构建

### 1. 文本分块规则 (`paragraph-v1-2048`)
- **短笔记**：整篇作为一个分块。
- **长笔记**：每块最多 2,048 个 Unicode 码点，优先按空行分段，其次按换行切分；超长单行按码点边界切分，保持 UTF-8 完整。
- **偏移记录**：每个分块记录在原文中的 UTF-8 字节区间 `[byte_start, byte_end)` 与起止行号，用于检索命中片段定位。

### 2. 索引代次（Generation）与空间隔离
- 向量索引存储在 `index.sqlite3`，可随时从 Memos 重建。
- 同一个向量空间内，笔记新增、编辑或删除只对变更条目执行单篇增量更新，无需全量重算。
- 更换向量供应商、模型或维度时，系统会分配新的索引代次并在后台批量构建；新代次就绪前，混合检索回退为关键词与标签排序，同空间旧星图快照在增量更新期间仍可读。

## 四、连接诊断

保存配置后，可在「设置 → AI」点击检查连接，或使用命令行核验网络、密钥与实际返回维度：

```bash
# 二进制环境
./build/moerise check-embedding --config .local/config.json

# Docker Compose 环境
docker compose exec -T moerise moerise check-embedding
```

成功输出示例：

```json
{
  "status": "ok",
  "provider": "openai-compatible",
  "protocol": "openai-compatible",
  "model": "text-embedding-3-small",
  "dimensions": 1536,
  "document_vectors": 2,
  "query_vectors": 1,
  "total_tokens": 18,
  "duration_ms": 620
}
```
