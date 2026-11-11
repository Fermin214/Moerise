# Moerise 向量模型 (Embedding)

[English](EMBEDDING.en.md)

Moerise 通过独立的向量提供商接口将笔记正文切块并转化为高维向量，为语义检索、混合检索、随机漫步以及 2D 认知地图提供计算底座。向量模型与对话模型（LLM）完全解耦，分别配置、独立启停。

---

## 一、支持的模型协议

Moerise 支持以下两类向量协议：

1. **OpenAI 兼容协议 (`openai-compatible`)**：兼容 OpenAI 官方 `text-embedding-3-*` 系列，以及各类遵循 OpenAI 规范的第三方网关或本地模型（如 Ollama、vLLM）。
2. **阿里云百炼 DashScope 原生协议**：`provider=bailian`、`protocol=dashscope`，适配 `qwen3.7-text-embedding` 的文档与查询向量。

---

## 二、配置示例

### 1. OpenAI 兼容协议配置

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

- `dimensions`：模型输出的向量维度（范围 1 至 16384）。必须与实际返回值严格相符。
- `send_dimensions`：针对支持显式指定输出维度的模型（如 OpenAI text-embedding-3），可设为 `true`；对固定维度模型保持 `false`。

### 2. 阿里云百炼配置

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

- `base_url`：包含 `/api/v1` 的 DashScope 基地址（北京公共端点或业务空间专属地址）。适配器将自动追加 `/services/embeddings/text-embedding/text-embedding`。
- `dimensions`：`qwen3.7-text-embedding` 原生支持 256, 512, 768, 1024, 1536, 2048, 2560 维（默认 1024）。

---

## 三、分块策略与向量索引构建

### 1. 文本分块规则 (`paragraph-v1-2048`)
- **短笔记处理**：正文字符较短的笔记整篇作为单个分块。
- **长笔记分块**：每块最多 2,048 个 Unicode 码点，优先在空行分段，其次在换行处切分；超长行沿码点边界切分，保持 UTF-8 完整。
- **元数据保留**：每个分块精确记录其在原始正文中的 UTF-8 字节偏移区间 `[byte_start, byte_end)` 以及对应的起止行号，确保检索高亮与定位严丝合缝。

### 2. 索引代次（Generation）与空间隔离
- 向量数据存储于 `index.sqlite3`，支持随时从 Memos 重新生成。
- 当用户在 Web 设置中更换向量模型、修改维度或更换 Provider 时，系统将分配全新的“空间代次”。
- 新代次在后台批量构建，只有模型空间与投影修订匹配的已发布代次可用于语义检索。更换模型空间后先降级为关键词与标签，待新索引就绪再启用语义；同空间旧地图快照在增量更新期间继续可读。

---

## 四、连接诊断与健康检查

在保存模型配置后，建议通过诊断命令核验连通性。该命令会发送微小的测试文本，验证端到端网络、身份凭证与返回向量维度。

**二进制环境：**
```bash
./build/moerise check-embedding --config .local/config.json
```

**Docker 容器环境：**
```bash
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
