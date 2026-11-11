# Moerise

**Write a thought. Come back to it. See where it leads.**

[简体中文](README.zh-CN.md) · [Get started](docs/GETTING-STARTED.md) · [Contribute](CONTRIBUTING.md)

Moerise is a self-hosted home for your personal notes, built on Memos. Capture an idea in a few lines, revisit something you wrote months ago, or follow a thread between notes you had forgotten were connected.

Writing is only the beginning. Moerise gives your notes a way back into your day.

## A few ways to spend time with your notes

- **Capture the moment.** Write in Markdown, add tags, drop in images or files. Browse your timeline and filter by date or tag when you want to find something again.
- **Meet an old thought.** Daily review brings a small selection of older notes back to you. Read them in the browser, or have them delivered through Feishu.
- **Take a walk.** Start anywhere, or from a particular note, and wander through related ideas. Each step offers another note you haven't visited on that walk.
- **See the shape of your collection.** The note map places similar notes closer together. Its daily snapshots show broad patterns; opening a point reads the live note.
- **Think with your notes.** Select the notes you want an AI to read, then ask questions, explore connections and follow up. Conversations are saved, and you can turn a completed reply into a new note.

Keyword search works on its own. Add an embedding model for semantic search, hybrid search, walks and the map. Add an LLM for AI conversations. Basic writing, reading and daily review work without either.

## Your notes stay in Memos

Memos holds your original notes, tags, attachments and account. Moerise reads and writes through its official API, so an existing Memos collection can be your starting point. Each Moerise instance connects to one author on a Memos 0.31.x instance.

Moerise Core handles search indexes, review schedules, conversations and settings. Product state can be backed up; indexes can be rebuilt. External tools can read through REST or MCP. If you enable a model provider, the notes needed for that feature are sent to the provider you configure.

The Web interface supports **English and Simplified Chinese**. It follows your browser's language on first visit, and you can switch languages from the login page or sidebar.

## Make it yours

You can run the full stack with Docker Compose or connect Moerise to Memos you already host. Linux binaries are also supported. Resource profiles are included for small servers, from 1 core / 1 GiB to 4 cores / 8 GiB.

```bash
git clone https://github.com/Fermin214/Moerise.git
cd Moerise
cp .env.example .env
docker compose up -d memos
```

Create your Memos account, bind it in `.env`, then start Moerise. The [getting started guide](docs/GETTING-STARTED.md) walks through these steps, including existing Memos instances and HTTPS deployment. After setup, open `http://localhost:3000` and sign in with your Memos account.

## Build with us

Moerise uses Go, SQLite and a small JavaScript frontend with no runtime dependencies. Bug reports, design feedback, translations and code contributions are welcome in English or Chinese.

Start with [CONTRIBUTING.md](CONTRIBUTING.md). The [roadmap](docs/ROADMAP.md) covers future work, including an Android app.

| Read more | |
| --- | --- |
| [Getting started](docs/GETTING-STARTED.md) | Installation and first use, in English |
| [Operations](docs/OPERATIONS.md) | Deployment, resource profiles, backups and upgrades, in Chinese |
| [Web](docs/WEB.md) | Interface behavior and Web deployment, in Chinese |
| [API v1](docs/API-V1.md) | Client APIs and read-only REST/MCP, in Chinese |
| [Architecture](docs/PRODUCT-ARCHITECTURE.md) | Components and data boundaries, in Chinese |

## License

[MIT](LICENSE) · Copyright (c) 2026 Fermin214. Memos styles retain their [upstream license](web/MEMOS-LICENSE).
