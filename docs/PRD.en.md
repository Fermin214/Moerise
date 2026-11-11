# Moerise Product Requirements (PRD)

[简体中文](PRD.md)

---

## 1. Product Positioning & Boundaries

### 1. Overview

Moerise is an open-source, self-hosted personal note enhancement product for one user. Memos provides the underlying note storage; Moerise builds on it for capture, reading, review, and reflection. Core provides review scheduling, semantic walks, cognitive maps, and AI insights, with Web, mobile, and messaging entry points for different daily workflows.

### 2. Core Principles

- **Simple, general defaults**: Keep basic notes clear, responsive, and reliable; expose useful personal preferences through configuration.
- **Preserve original content**: Native capture saves entered text and attachments verbatim, without implicit rewriting, forced formatting, filenames, or placeholders. Native `#tags` follow Memos behavior; rule changes do not rewrite historical notes in bulk.
- **AI as a thinking aid**: Insights are a separate exploration feature. Users decide which generated replies or conversations to save to Memos.
- **Storage boundaries**: Memos owns original notes, tags, images, and attachments. Core stores indexes, settings, review plans and history, AI conversations, and messaging state.

### 3. Identity & Permissions

- Use Memos accounts for authentication and note ownership.
- Bind each instance to one authenticated author. Administrator PATs remain subject to the same author boundary.

---

## 2. Entry Points & Basic Notes

### 1. Moerise Web

- **Timeline and editing**: A light two-column layout, top composer, Markdown, attachment input, save/cancel shortcuts, and inline editing.
- **Combined filters**: Navigation, search, calendar, and tags in the sidebar. Search terms, dates, and tags combine; applied filters can be cleared individually or together.
- **Note details**: Each note has a dedicated URL, with entry points for a walk or AI insight. Returning restores filters and viewport.
- **Basic and enhanced search**: Model-independent keyword/tag search, plus explicitly enabled semantic and hybrid search.

### 2. Android (Planned)

A mobile capture and review client using shared Core APIs, with a mobile timeline and offline note capture.

### 3. External MCP & REST

Read-only tools for external agents to search and read notes, with server-side restrictions on writes.

---

## 3. Core Features

### 1. Daily Review

- **Goal**: Rediscover earlier thoughts by reading original notes.
- **Selection**:
  - Maturity delay, default 72 hours since creation.
  - Recent-selection exclusion, default 7 days.
  - At least text or an image; non-image attachment-only notes are excluded.
  - Configurable inclusion of archived notes.
- **Fixed full-day plan**: A daily plan with distinct subsets allocated across configured times, such as `09:00` and `18:00`, unchanged by refresh.
- **Low-pressure reading**: No unread badges, streaks, or completion confirmations.
- **Delivery**: Scheduled Telegram bot or Feishu bot/webhook reviews.

### 2. Random Walk

- **Goal**: Explore semantic connections beyond tags and categories.
- **Flow**: Start from a specified or random note and view one note at a time; Continue selects another eligible semantic candidate.
- **Deduplication**: Do not revisit a note in the same walk until the walk ends or the user exits.

### 3. AI Insights

- **Goal**: Select notes for follow-up questions, contradictions, hypotheses, and brainstorming.
- **Materials**: One or more normal or archived notes, with controlled body size of 20,000 Unicode code points.
- **Interaction**: SSE replies, multi-turn discussion, active cancellation, and persistent conversation history.
- **Saving**: Save a chosen assistant reply as plain text/Markdown to Memos.

### 4. Cognitive Map

- **Goal**: Visualize long-term themes and concentrations of thought in two dimensions.
- **Interaction**: PCA projection and local spiral placement create stable coordinate snapshots, with pan, zoom, keyboard controls, and live note reading.

---

## 4. Messaging & Notifications

1. **Feishu app bot**: Private text, image, and file capture to Memos; scheduled long-text and image reviews.
2. **Feishu group webhook**: Scheduled reviews to a group.
3. **Telegram bot**: Reviews with an English header to a chat ID, ordered text chunks, and original image files.
4. **Delivery state**: `pending`, `running`, `unknown`, and `sent`, with backoff and owner resolution of uncertain attempts.

---

## 5. Deployment & Data

1. **Deployment**: Bundled Docker Compose with Memos 0.31.x, Core, and Web; existing Memos connections; native Linux server binaries.
2. **Databases**:
   - `state.sqlite3`: Credentials, settings, conversations, reviews, and notifications; requires backups.
   - `index.sqlite3`: FTS5, vectors, and map snapshots; rebuildable from Memos.
3. **Credentials**: Encrypt model keys and bot tokens with AES-256-GCM and never return plaintext secrets to the browser.
