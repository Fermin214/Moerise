# Moerise

**A self-hosted personal note enhancement system built on Memos.**

[简体中文](README.md) · [Getting Started](docs/GETTING-STARTED.md) · [Operations](docs/OPERATIONS.en.md) · [API Documentation](docs/API-V1.en.md) · [Contributing](CONTRIBUTING.md)

---

Moerise uses [Memos](https://usememos.com/) as its underlying note storage, providing an enhanced personal knowledge experience around capturing, reading, reviewing, and reflective thinking. It complements Memos with a modern bilingual Web interface, hybrid full-text and vector search, an exploratory "Random Walk", a 2D "Cognitive Map", context-aware AI conversations, and low-friction multi-channel "Daily Reviews" (via Telegram and Feishu).

Your raw notes, tags, and media attachments always remain authoritative inside Memos. Moerise reads and writes exclusively through Memos' official APIs and preserves the content you enter.

---

## Key Features

- 📝 **Native Note Management & Modern Web UI**
  - Clean two-column light-theme Web interface with built-in, adaptive support for **English and Simplified Chinese**.
  - Markdown writing, tag filtering, image and file attachments, in-card quick editing, and dedicated detail view routes.
- 🔍 **Dual-Engine Search**
  - Live keyword filtering in the timeline, plus SQLite FTS5 search with Chinese segmentation through machine REST/MCP and hybrid search.
  - Optional vector embeddings (supporting OpenAI-compatible providers and Alibaba Bailian) enabling semantic search and Reciprocal Rank Fusion (RRF) hybrid retrieval.
- 🧭 **Random Walk**
  - Break free from rigid folder hierarchies and tag silos: wander through related notes starting from an arbitrary note or a random seed.
  - Controlled semantic sampling with single-walk deduplication to spark unexpected cross-domain associations.
- 🗺️ **Cognitive Map**
  - Dimensionally projects high-dimensional note vectors onto an interactive 2D map, visualizing thematic clusters and thought patterns.
  - Builds snapshots in the background, combining ordinary changes at 24-hour intervals; supports pan/zoom and direct note opening.
- 💬 **Context-Aware AI Insights**
  - Handpick any combination of notes as contextual material to engage in streaming multi-turn dialogues, critique, and brainstorming.
  - Compatible with OpenAI Chat Completions, OpenAI Responses, and Anthropic Messages protocols; supports customizable prompt templates and one-click saving back to Memos.
- 📬 **Stress-Free Daily Review**
  - Randomly selects older notes based on configurable maturity delays (e.g. at least 3 days old) and recent repetition filters.
  - Saves a fixed full-day plan distributed across configured time slots; no unread badges, streaks, or mandatory checklist pressures.
  - Optional scheduled delivery through **Telegram bots** or **Feishu bots / webhooks**.
  - New installations have no messaging channel selected; choose either channel in Settings.
- 🔌 **Open Ecosystem & Agent Connectivity**
  - Provides a read-only Model Context Protocol (MCP) server alongside machine REST APIs.
  - Enables external tools such as Claude Desktop and Cursor to safely search and inspect your notes under strict server-side read-only whitelists.
- 🛡️ **Strict Data Sovereignty & Dual-Database Architecture**
  - Each Moerise instance strictly binds to a single Memos author account.
  - Clean separation: `state.sqlite3` holds user settings, AI chats, and review logs; `index.sqlite3` maintains disposable, fully rebuildable search and map indexes.

Basic notes, reading, keyword filtering, and daily review work without models. Semantic search, walks, and the map require Embedding; AI conversations require an LLM. Enabled model features send the relevant note content to the provider you configure.

---

## Architecture Overview

```mermaid
flowchart LR
    Browser["Web Browser"] -->|"Same-Origin Cookie / API"| Web["Moerise Web Server"]
    Web -->|"Fixed API Proxy"| Core["Moerise Core Server"]
    Agent["External Agent / MCP Client"] -->|"Read-Only Bearer Token"| Core
    Feishu["Feishu Platform"] <-->|"Event Callbacks / Delivery"| Core
    Core -->|"Scheduled Delivery"| Telegram["Telegram Platform"]

    Core -->|"Official API / Sync"| Memos["Official Memos (0.31.x)"]
    Core --> State["state.sqlite3 (Application State)"]
    Core --> Index["index.sqlite3 (Rebuildable Indexes)"]
    Core -.-> Embedding["Embedding Provider (Optional)"]
    Core -.-> LLM["LLM Provider (Optional)"]
```

---

## Quick Start

The recommended deployment method uses Docker Compose, which includes an official Memos 0.31.x instance, or connects Moerise to an existing Memos deployment.

### 1. Launch All-in-One Stack

```bash
# Clone repository
git clone https://github.com/Fermin214/Moerise.git
cd Moerise

# Prepare configuration
cp .env.example .env

# Start Memos first to set up your author account
docker compose up -d memos
```

1. Open `http://localhost:5231` in your browser and complete initial Memos registration.
2. In Memos **Settings → Access Tokens**, create a Personal Access Token (PAT).
3. Request `http://localhost:5231/api/v1/auth/me` with your PAT to find your exact author resource name (e.g. `users/abc123`).
4. Update `.env` with your Memos PAT, user resource name, and generated encryption secrets (see [Getting Started](docs/GETTING-STARTED.md)).

### 2. Start Moerise

```bash
docker compose up -d --build
```

Once running, navigate to `http://localhost:3000` and sign in using your Memos username and password.

> For detailed environment variables, existing Memos connections, HTTPS reverse proxy configuration, and server resource profiles, see [Operations Guide](docs/OPERATIONS.en.md).

---

## Documentation

[English / Chinese Documentation Index](docs/README.md)

| Topic | Document | Description |
| --- | --- | --- |
| **Setup & Operations** | [Getting Started](docs/GETTING-STARTED.md) | First-time setup, Docker Compose, and account binding |
| | [Operations](docs/OPERATIONS.en.md) | Deployment, resource profiles, backups, and recovery procedures |
| **User & Features** | [Web Interface](docs/WEB.en.md) | UI layout, bilingual settings, same-origin model, and security |
| | [Daily Review](docs/DAILY-REVIEW.en.md) | Review selection logic, schedule allocation, and reading flow |
| | [Feishu Integration](docs/FEISHU.en.md) | Feishu bot note capture, review notifications, and webhooks |
| | [Telegram Delivery](docs/TELEGRAM.md) | Scheduled review delivery via Telegram bots |
| | [AI & Insights](docs/AI.en.md) | LLM configuration, contextual token limits, and connection health |
| | [Embeddings](docs/EMBEDDING.en.md) | Vector models (OpenAI / Bailian), chunking, and index generation |
| | [Search Engine](docs/SEARCH.en.md) | FTS5 full-text search, cosine similarity, and RRF fusion |
| | [System Settings](docs/SETTINGS.en.md) | Versioned runtime settings and credential encryption |
| **Architecture** | [Product Architecture](docs/PRODUCT-ARCHITECTURE.en.md) | Component responsibilities, dual-database model, and boundaries |
| | [Codebase Guide](docs/CODEBASE.en.md) | Source entry points, directory responsibilities, and change guidance |
| | [Development Scripts](scripts/README.en.md) | Cross-service acceptance, benchmarks, and quality evaluation |
| | [System Design](docs/DESIGN.en.md) | Web integration, API boundaries, and communication architecture |
| | [API Specification](docs/API-V1.en.md) | Client API `/api/app/v1`, machine REST `/api/ext/v1`, and `/mcp` |
| | [Development Guide](docs/DEVELOPMENT.en.md) | Local development workflow, test suites, and verification |
| | [Roadmap](docs/ROADMAP.en.md) | Completed capabilities and upcoming milestones (including Android) |
| | [Product Requirements](docs/PRD.en.md) | Product positioning, feature specifications, and entry-point boundaries |

---

## License

Distributed under the [MIT License](LICENSE). Reused Memos styles retain their [upstream license](web/MEMOS-LICENSE).
