# Moerise Settings & Credential Management

[简体中文](SETTINGS.md)

Moerise separates deployment infrastructure from versioned persistent settings. Daily preferences and model credentials can be changed in Web and saved in encrypted product state.

---

## 1. Configuration Layers & Precedence

1. **Deployment infrastructure**:
   - Memos origins, author, PAT, public origin (`MOERISE_PUBLIC_ORIGIN` for Core and `PUBLIC_ORIGIN` for Web), listen ports, data directories, and `MOERISE_STATE_ENCRYPTION_KEY`.
   - Supplied through environment variables or JSON and read at startup.
2. **Persistent product settings**:
   - Timezone, review rules and times, model providers and API keys, Feishu/Telegram credentials, and custom insight prompts.
   - Initialized from deployment values once and then read from `state.sqlite3`. Models, channels, and prompts take effect after saving; timezone, review rules, and delivery times apply on the next local day, preserving the existing daily plan. Restarts and index rebuilding preserve settings.

---

## 2. Encrypted Credentials

Model keys, bot tokens, and webhook secrets are encrypted server-side with AES-256-GCM:

- Keys are derived from `MOERISE_STATE_ENCRYPTION_KEY`.
- Settings responses expose only `configured` status, never plaintext secrets.
- Retain the encryption key with database backups so stored credentials can be recovered.

---

## 3. Updates & Runtime Replacement

### 1. Read Settings

- Request: `GET /api/app/v1/settings`.
- Response: `version`, settings objects, secret status, and text/image/capture channel capabilities.

### 2. Update Settings

- Request: `PATCH /api/app/v1/settings`.
- Supply the last-read `version`, changed fields in `settings`, and credential operations in `secrets`.
- **Secret operations**:
  - `{"action":"keep"}`: Keep the existing value.
  - `{"action":"replace","value":"new-secret"}`: Replace it.
  - `{"action":"clear"}`: Clear it.
- **Version conflicts**: A stale version returns 409 `SETTINGS_CONFLICT`, preventing one client's changes from overwriting another's.

### 3. Drain & Replace

- Core waits up to five seconds for operations using old settings to drain, then replaces affected model and channel clients. A timeout returns retryable `SETTINGS_BUSY`, leaving the old settings active.
- Changing the LLM does not rebuild vectors. Changing the Embedding model space starts a new vector generation.
