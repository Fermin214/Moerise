# Moerise API v1 Specification

[简体中文](API-V1.md)

Moerise provides two REST API groups and a Model Context Protocol (MCP) endpoint:

1. **Client API (`/api/app/v1`)**: For Moerise Web and future mobile clients, using same-origin session cookies and CSRF tokens.
2. **Machine API (`/api/ext/v1`)**: For scripts and third-party integrations, using Bearer tokens with **strictly read-only** operations.
3. **MCP (`/mcp`)**: For external AI tools supporting MCP, with **strictly read-only** tools.

The complete contract is embedded in `/openapi.json`, with interactive documentation at `/docs` on the Core listener.

---

## 1. Client API (`/api/app/v1`)

The client API uses a session cookie. Login checks `Origin`; authenticated non-read requests also require `X-CSRF-Token`. Machine Bearer tokens cannot authenticate client requests.

### 1. Authentication & Sessions

| Method | Path | Behavior |
| --- | --- | --- |
| `GET` | `/api/app/v1/capabilities` | Supported login methods and note capabilities |
| `POST` | `/api/app/v1/session` | Sign in with `username` and `password`; return identity and `csrf_token`, and issue a cookie |
| `GET` | `/api/app/v1/session` | Validate the session and return identity, timezone, and expiration |
| `DELETE` | `/api/app/v1/session` | Revoke the session and clear the cookie |

### 2. Notes & Timeline

| Method | Path | Behavior |
| --- | --- | --- |
| `GET` | `/api/app/v1/notes` | Page through notes by creation time, with tag, date, and archive filters |
| `GET` | `/api/app/v1/notes/{memo_id}` | Read a live note and its attachments |
| `POST` | `/api/app/v1/notes` | Create a note from content and optional attachments; requires `Idempotency-Key` |
| `PATCH` | `/api/app/v1/notes/{memo_id}` | Edit content, tags, attachments, or archive state; requires `Idempotency-Key` |
| `DELETE` | `/api/app/v1/notes/{memo_id}` | Delete a note; requires `source_hash` in the JSON body |
| `GET` | `/api/app/v1/tags` | Read tags and associated note counts |
| `GET` | `/api/app/v1/calendar` | Read daily note counts for a month |
| `GET`, `HEAD` | `/api/app/v1/notes/{memo_id}/attachments/{attachment_id}/content` | Protected attachment stream with Range support |

### 3. Search, Walks & Map

| Method | Path | Behavior |
| --- | --- | --- |
| `POST` | `/api/app/v1/search` | Default `keyword` reads live Memos filters ordered by creation time; `semantic` and `hybrid` use indexes and relevance paging |
| `POST` | `/api/app/v1/walks/start` | Start from a specified note or a random seed |
| `POST` | `/api/app/v1/walks/next` | Submit the path and select an unvisited semantic candidate |
| `GET` | `/api/app/v1/map` | Read the latest published 2D map snapshot, including coordinates and tags |

### 4. Daily Review

| Method | Path | Behavior |
| --- | --- | --- |
| `GET` | `/api/app/v1/reviews/daily` | Read the fixed full-day plan and selected notes |

### 5. AI Insights & Conversations

| Method | Path | Behavior |
| --- | --- | --- |
| `GET` | `/api/app/v1/chats` | Page through saved conversations |
| `POST` | `/api/app/v1/chats` | Create a conversation with a title and selected note materials |
| `GET` | `/api/app/v1/chats/{chat_id}` | Read the conversation and message history |
| `PATCH` | `/api/app/v1/chats/{chat_id}` | Update title or material resource names `names`, using `version` |
| `DELETE` | `/api/app/v1/chats/{chat_id}` | Delete a conversation using `version` |
| `GET` | `/api/app/v1/chat-materials` | Page through live normal or archived notes for selection |
| `POST` | `/api/app/v1/chat-materials/resolve` | Submit `names`, verify source text, and return material sizes and hashes |
| `POST` | `/api/app/v1/chats/{chat_id}/messages` | Generate a reply as a Server-Sent Events (SSE) stream |
| `POST` | `/api/app/v1/chats/{chat_id}/messages/{request_id}/cancel` | Stop an active generation |
| `POST` | `/api/app/v1/chats/{chat_id}/messages/{request_id}/save` | Save an assistant reply unchanged as a Memos note |

### 6. Settings & Diagnostics

| Method | Path | Behavior |
| --- | --- | --- |
| `GET` | `/api/app/v1/settings` | Read settings, secret status, and channel capabilities |
| `PATCH` | `/api/app/v1/settings` | Update settings and credentials using a settings version |
| `GET` | `/api/app/v1/models` | Read model configuration and vector-index progress |
| `POST` | `/api/app/v1/models/llm/check` | Check LLM connectivity |
| `POST` | `/api/app/v1/models/embedding/check` | Check Embedding connectivity and dimensions |

---

## 2. Machine API (`/api/ext/v1`)

Authenticate with `Authorization: Bearer <MOERISE_ACCESS_TOKEN>`. The server enforces a read-only operation whitelist:

| Method | Path | Scope | Behavior |
| --- | --- | --- | --- |
| `GET` | `/api/ext/v1/notes` | `notes:read` | Page through the bound author's notes |
| `GET` | `/api/ext/v1/notes/{memo_id}` | `notes:read` | Read live text and metadata |
| `POST` | `/api/ext/v1/search` | `notes:read` | Keyword, semantic, or hybrid search; default `hybrid` |
| `GET` | `/api/ext/v1/notes/{memo_id}/related` | `notes:read` | Find related notes using keywords, tags, and existing vectors, without model calls |
| `GET` | `/api/ext/v1/reviews/daily` | `reviews:read` | Read an existing daily review without creating it |
| `GET` | `/api/ext/v1/reviews/{batch_id}` | `reviews:read` | Read an existing batch with source verification |
| `GET` | `/api/ext/v1/sync` | `sync:read` | Read synchronization watermarks and vector-index status |
| `GET` | `/api/ext/v1/search/index` | `notes:read` | Inspect vector construction and published generations |
| `GET` | `/api/ext/v1/me` | `notes:read` | Read instance binding, credential name, and scopes |
| `GET` | `/api/ext/v1/jobs/{job_id}` | `sync:read` | Inspect an existing synchronization job |
| `POST` | `/api/ext/v1/discovery/walk` | `notes:read` | Read-only semantic walk with a seed and path |

---

## 3. Model Context Protocol (`/mcp`)

Moerise implements MCP for external AI tools to read and search personal notes.

- **Read-only boundary**: Tools cannot create, edit, or delete notes, or modify settings.
- **Transport**: Stateless Streamable HTTP with JSON responses. Clients must support remote HTTP MCP and provide a Bearer token.
- **Tools**:
  - `semantic_search`: Multi-mode search; requires `notes:read`.
  - `get_memo`: Read a note by `name`, such as `memos/abc123`; requires `notes:read`.
  - `list_memos`: Page through notes; requires `notes:read`.
  - `get_related_memos`: Find related notes; requires `notes:read`.
  - `semantic_walk`: Start randomly or move through semantic candidates; requires `notes:read`.
  - `get_review_batch`: Read an existing `batch_id`; requires `reviews:read`.
  - `read_daily_review`: Read an existing daily review; requires `reviews:read`.

## 4. Requests & Failure Handling

- Note creation accepts `content` and optional `attachments`, with at most 65,536 UTF-8 bytes of text. Tags remain in the body and are parsed by Memos. Editing requires the latest `source_hash` and accepts `content`, `state`, `attachments`, and `remove_attachments`.
- After an uncertain create or edit result, retry with the same `Idempotency-Key` and complete body. Reusing a key with different content returns a conflict. Deletion requires a matching source hash.
- Conversation creation requires `request_id`; generation requires `request_id`, `version`, `kind`, and applicable `content` and `language`. Retrying an ID reuses its turn. Saving a reply requires the assistant message's `version`.
- Model checks require the settings `version`; reread it after a settings update. Notification management and resolution requests are described in [Notifications](NOTIFICATIONS.en.md).
- Errors use `{"error":{"code":"...","message":"...","retryable":false,"request_id":"..."}}`. Source failures do not return cached text. Machine REST/MCP returns `REVIEW_NOT_FOUND` when no daily plan exists.
