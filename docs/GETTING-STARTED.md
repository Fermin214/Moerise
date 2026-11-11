# Getting started with Moerise

[Product introduction](../README.md) · [中文安装指南](OPERATIONS.md#容器部署)

Moerise runs on Linux, directly or in Docker Linux containers. Docker deployment needs Docker Engine and Compose 2.24 or later. The bundled stack includes Memos 0.31.x, Moerise Core and the Web interface. Each installation serves one Memos author.

## Start with a new Memos instance

Run these commands from the repository root:

```bash
cp .env.example .env
docker compose up -d memos
```

Open `http://localhost:5231` and create a Memos account. In Memos account settings, create a personal access token (PAT). Request `GET http://localhost:5231/api/v1/auth/me` with `Authorization: Bearer <your-PAT>` and find `user.name` in the response. This is the resource name, such as `users/abc123`, rather than your sign-in username.

Edit `.env`:

| Variable | Value |
| --- | --- |
| `MOERISE_MEMOS_TOKEN` | Your Memos PAT |
| `MOERISE_MEMOS_USER` | The exact `user.name` from `/api/v1/auth/me` |
| `MOERISE_MEMOS_INSTANCE_ID` | A unique, stable identifier you choose for this Memos instance |
| `MOERISE_TIMEZONE` | An IANA timezone, such as `Europe/London` or `America/Chicago` |
| `MOERISE_ACCESS_TOKEN` | A separate random token for machine access |
| `MOERISE_CURSOR_SECRET` | A separate random secret for pagination |
| `MOERISE_STATE_ENCRYPTION_KEY` | A base64-encoded 32-byte key for saved credentials |

Generate the last three values independently:

```bash
openssl rand -hex 32     # MOERISE_ACCESS_TOKEN
openssl rand -hex 32     # MOERISE_CURSOR_SECRET
openssl rand -base64 32  # MOERISE_STATE_ENCRYPTION_KEY
```

Keep the instance ID, Compose project name and encryption key stable across upgrades. Back up `.env` securely along with product state; losing the encryption key prevents recovery of saved credentials.

Start the complete stack:

```bash
docker compose up -d --build
docker compose ps
curl --fail http://localhost:3000/health/ready
```

Open `http://localhost:3000` and sign in with the bound Memos account's username and password. Memos must allow password sign-in. The interface uses your browser language; choose English or 简体中文 from the language selector to override it.

## Connect an existing Memos instance

Use Memos 0.31.x and the PAT and resource name of the author whose notes you want to use. In `.env`, set:

```dotenv
COMPOSE_PROFILES=
MOERISE_MEMOS_BASE_URL=https://memos.example.com
MOERISE_MEMOS_PUBLIC_URL=https://memos.example.com
```

`MOERISE_MEMOS_BASE_URL` must be reachable from the Core container. `localhost` inside a container refers to that container. `MOERISE_MEMOS_PUBLIC_URL` is the browser-facing Memos origin. The origins may differ, for example when Core uses a private container network.

Fill in the same author binding, independent secrets and timezone described above, then run `docker compose up -d --build`. Moerise uses Memos' official API; it does not need direct access to the Memos database.

## Read, write and explore

Start with ordinary notes, attachments, tags, keyword search and daily review. Open **Settings** to configure optional services:

- **Embeddings:** choose a supported provider and model, save, check the connection, then wait for the semantic index to finish. This enables semantic search, hybrid search, random walks and the note map.
- **LLM:** configure OpenAI Chat Completions, OpenAI Responses or Anthropic Messages. Save and check the connection. In AI insights, select notes and ask a question or choose Generate insight. Embeddings are not required for chat.
- **Feishu:** configure a bot or webhook for optional review delivery. Feishu's bot commands and delivery messages use Chinese; the Web interface works independently of this integration.

Model features send the necessary note content to the provider you configure. Notes, tags, custom prompts and existing conversations retain their original language. New AI requests carry the interface's reply language; an explicit user request can ask for another language.

## Expose the Web interface over HTTPS

The default ports bind to loopback. For remote access, place an HTTPS reverse proxy in front of Web and set, for example:

```dotenv
MOERISE_PUBLIC_ORIGIN=https://notes.example.com
```

Forward that origin to Web's port 3000. If the proxy runs in another container, connect it to Web's network or configure `WEB_BIND_ADDRESS` for the intended private network. Remote public origins require HTTPS; plain HTTP is accepted only for loopback addresses. Restart the stack after changing infrastructure variables.

Web proxies only `/api/app/v1`. Machine REST/MCP, Core API documentation and Feishu callbacks use the separate Core listener on port 8787. Expose only the endpoints your deployment needs through your reverse proxy.

## Choose a resource profile

Core adjusts its memory budget, source read batches and computation concurrency to process limits. Compose profiles provide service limits for these server sizes:

| Server | Resource file |
| --- | --- |
| 1 core / 1 GiB | `deploy/resources/1c1g.env` |
| 2 cores / 1 GiB | `deploy/resources/2c1g.env` |
| 2 cores / 2–4 GiB | `deploy/resources/2c2g.env` |
| 4 cores / 4–8 GiB | `deploy/resources/4c4g.env` |

Copy the resource file's entries into your `.env`. These are service limits, rather than measured idle usage; leave room for the operating system and other services. The 2-core / 2–4 GiB profile is suitable for choosing initial limits on a 2C4G host.

## Back up and upgrade

Memos data and Moerise product state both need backups. Compose stores Memos, product state, rebuildable indexes and backup output in separate named volumes. Rebuilding an index preserves chats, settings and review history. A product-state backup does not replace a backup of Memos notes and attachments.

Keep `.env`, the instance ID and the Compose project name stable. Before an upgrade, back up both services and the encryption key. Pull the intended source revision and rebuild with `docker compose up -d --build`. Detailed backup, restore and rollback commands are in [Operations](OPERATIONS.md).
