# Moerise Embedding Models

[简体中文](EMBEDDING.md)

An independent Embedding provider turns note chunks into vectors for semantic search, hybrid search, Random Walk, and the 2D Cognitive Map. Embedding and LLM models are configured and enabled separately.

---

## 1. Supported Protocols

1. **OpenAI-compatible**: OpenAI embedding models and gateways or local servers that implement the protocol.
2. **Bailian DashScope**: `provider=bailian`, `protocol=dashscope`, with document and query support for `qwen3.7-text-embedding`.

---

## 2. Configuration Examples

### 1. OpenAI-Compatible

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

- `dimensions`: Expected output width, 1–16384; it must match the returned vectors.
- `send_dimensions`: Set `true` for models supporting explicit output dimensions; leave `false` for fixed-width models.

### 2. Bailian

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

- `base_url`: DashScope base ending in `/api/v1`. The adapter appends `/services/embeddings/text-embedding/text-embedding`.
- `dimensions`: 256, 512, 768, 1024, 1536, 2048, or 2560; default 1024.

---

## 3. Chunking & Vector Generations

### 1. Chunking (`paragraph-v1-2048`)

- **Short notes**: Keep the body in one chunk.
- **Long notes**: At most 2,048 Unicode code points per chunk, preferring blank-line boundaries, then newlines. Long lines split at code-point boundaries without corrupting UTF-8.
- **Locations**: Chunks record original UTF-8 byte ranges `[byte_start, byte_end)` and line numbers for source highlighting.

### 2. Generations & Model Spaces

- Vectors live in rebuildable `index.sqlite3`.
- Changing the model, dimensions, or provider changes the model space and triggers construction.
- Only a published generation matching the configured space and projection revision can serve semantic retrieval. A new model space uses keyword/tag fallback until ready. Existing map snapshots in the same space remain readable during incremental indexing.

---

## 4. Connection Checks

Checks send small test texts and verify connectivity, credentials, vector count, and dimensions:

```bash
./build/moerise check-embedding --config .local/config.json
```

In Docker:

```bash
docker compose exec -T moerise moerise check-embedding
```

Example output:

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
