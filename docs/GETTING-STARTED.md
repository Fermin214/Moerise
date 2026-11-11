# Getting Started with Moerise

[Product Overview](../README.md) · [中文运行指南](OPERATIONS.md#容器部署)

Moerise runs on Linux natively or via Docker Linux containers. Docker deployment requires Docker Engine and Docker Compose (2.24 or later). The bundled stack includes Memos 0.31.x, Moerise Core, and the Moerise Web frontend. Each Moerise instance connects to one author on a Memos instance.

---

## 1. Start with a New Memos Instance

Run these commands from the repository root:

```bash
cp .env.example .env
docker compose up -d memos
```

1. Open `http://localhost:5231` in your browser and register your Memos account.
2. In Memos **Settings → Access Tokens**, generate a Personal Access Token (PAT).
3. Request the Memos profile endpoint to find your exact author resource name:
   ```bash
   curl -H "Authorization: Bearer <your-PAT>" http://localhost:5231/api/v1/auth/me
   ```
   Note the `user.name` property in the JSON response (e.g. `users/abc123`). This is the system resource identifier, distinct from your sign-in username.

4. Edit `.env` to configure your instance:

| Variable | Description |
| --- | --- |
| `MOERISE_MEMOS_TOKEN` | Your Memos PAT |
| `MOERISE_MEMOS_USER` | The exact `user.name` string from `/api/v1/auth/me` |
| `MOERISE_MEMOS_INSTANCE_ID` | A stable, unique identifier for this instance (e.g. `primary-memos`) |
| `MOERISE_TIMEZONE` | IANA timezone (e.g. `Asia/Shanghai`, `Europe/London`, `America/New_York`) |
| `MOERISE_ACCESS_TOKEN` | Independent secret token for external machine/MCP access |
| `MOERISE_CURSOR_SECRET` | Independent random secret used for pagination signatures |
| `MOERISE_STATE_ENCRYPTION_KEY` | Base64-encoded 32-byte key for encrypting stored credentials |

Generate independent cryptographic keys using OpenSSL:

```bash
openssl rand -hex 32     # MOERISE_ACCESS_TOKEN
openssl rand -hex 32     # MOERISE_CURSOR_SECRET
openssl rand -base64 32  # MOERISE_STATE_ENCRYPTION_KEY
```

> [!IMPORTANT]
> Keep your `.env` and `MOERISE_STATE_ENCRYPTION_KEY` safely backed up. If the encryption key is lost, encrypted provider credentials and notification secrets cannot be recovered.

5. Start the complete stack:

```bash
docker compose up -d --build
docker compose ps
curl --fail http://localhost:3000/health/ready
```

Open `http://localhost:3000` and sign in using your Memos username and password. The interface detects your browser language preference; you can toggle between English and 简体中文 via the sidebar or login page.

---

## 2. Connect to an Existing Memos Instance

To connect Moerise to a pre-existing Memos (0.31.x) instance:

1. Obtain the PAT and `user.name` for the target author account.
2. In `.env`, set:

```dotenv
COMPOSE_PROFILES=
MOERISE_MEMOS_BASE_URL=https://memos.example.com
MOERISE_MEMOS_PUBLIC_URL=https://memos.example.com
```

- `MOERISE_MEMOS_BASE_URL`: The URL reachable from inside the Moerise Core container.
- `MOERISE_MEMOS_PUBLIC_URL`: The public-facing URL your browser uses to open original Memos links.

3. Fill in the remaining required secrets (`MOERISE_ACCESS_TOKEN`, `MOERISE_CURSOR_SECRET`, `MOERISE_STATE_ENCRYPTION_KEY`) and launch with:

```bash
docker compose up -d --build
```

---

## 3. Configuring Optional Features

Core writing, reading, keyword search, and daily reviews work immediately without third-party models or messaging integrations. Open **Settings** in the Web UI to enable optional services:

- **Embeddings:** Configure an OpenAI-compatible endpoint or Alibaba Bailian DashScope. Verify the connection, then wait for the semantic index build to complete. This activates semantic search, hybrid search, the random walk, and the cognitive map.
- **LLM:** Configure OpenAI Chat Completions, OpenAI Responses, or Anthropic Messages. Verify connectivity. In AI Insights, select notes and start multi-turn discussions. Embeddings are not required for chat.
- **Messaging Channels:** Choose [Telegram](TELEGRAM.md) for scheduled review delivery in English, or [Feishu](FEISHU.md) for Chinese delivery and note capturing. New installations start with messaging disabled.

---

## 4. Reverse Proxy & HTTPS Deployment

Default Compose bindings expose ports to the local loopback interface. For production deployments with public domains, place an HTTPS reverse proxy (such as Nginx, Caddy, or Traefik) in front of Moerise Web and set:

```dotenv
MOERISE_PUBLIC_ORIGIN=https://notes.example.com
```

Forward traffic for `notes.example.com` to Moerise Web port `3000`. Web proxies `/api/app/v1` internally to Core. For external MCP or machine REST endpoints (`/api/ext/v1` and `/mcp`), forward requests directly to Core port `8787` as needed.

---

## 5. Resource Profiles

Moerise Core automatically tunes memory budgets and concurrency limits based on container cgroups. Sample resource configurations for various host sizes are available in `deploy/resources/`:

| Server Size | Profile File |
| --- | --- |
| 1 Core / 1 GiB | `deploy/resources/1c1g.env` |
| 2 Cores / 1 GiB | `deploy/resources/2c1g.env` |
| 2 Cores / 2–4 GiB | `deploy/resources/2c2g.env` |
| 4 Cores / 4–8 GiB | `deploy/resources/4c4g.env` |

Copy the contents of the appropriate profile file into your `.env` to enforce resource bounds.

---

## 6. Backups and Upgrades

Memos data and Moerise product state are stored in isolated named Docker volumes:
- Memos database: `memos-data`
- Moerise state: `moerise-state` (contains `state.sqlite3`)
- Moerise index: `moerise-index` (contains `index.sqlite3`, rebuildable)

To upgrade Moerise:
1. Back up `.env` and `moerise-state`.
2. Pull the latest repository updates:
   ```bash
   git pull origin main
   docker compose up -d --build
   ```

For detailed backup and recovery procedures, see [Operations Guide](OPERATIONS.md).
