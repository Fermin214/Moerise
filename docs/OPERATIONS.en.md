# Moerise Operations, Maintenance & Recovery

[简体中文](OPERATIONS.md)

Moerise supports native Linux binaries and Docker containers. This guide covers production configuration, container deployment, server resource profiles, backups, recovery, and index rebuilding.

---

## 1. Configuration & Environment Variables

Configuration templates are provided in `.env.example` and `config.example.json` at the repository root. The table lists common environment variables; nonempty values override JSON. Daily settings, including models and messaging channels, read deployment values only during initialization, then use encrypted persistent settings.

| Environment Variable | JSON Key | Default | Description & Constraints |
| --- | --- | --- | --- |
| `MOERISE_MEMOS_INSTANCE_ID` | `memos.instance_id` | Required | Unique, stable Memos instance identifier, 8–128 characters |
| `MOERISE_MEMOS_BASE_URL` | `memos.base_url` | Required | HTTP(S) origin used by Core to access Memos |
| `MOERISE_MEMOS_PUBLIC_URL` | `memos.public_url` | Required | Browser-facing origin for original Memos links |
| `MOERISE_MEMOS_USER` | `memos.user` | Required | Bound author resource name, such as `users/abc123` |
| `MOERISE_MEMOS_TOKEN` | `memos.token` | Required | Memos Personal Access Token (PAT) for the bound account |
| `MOERISE_ACCESS_TOKEN` | `access_tokens` | Required | Bearer token for machine REST and MCP access |
| `MOERISE_CURSOR_SECRET` | `cursor_secret` | Required | Independent random secret for cursor signatures and resource hashes, at least 32 characters |
| `MOERISE_STATE_ENCRYPTION_KEY` | `web.state_encryption_key` | Required for Web or Feishu app bots | Base64-encoded 32-byte key for stored credentials |
| `MOERISE_WEB_ENABLED` | `web.enabled` | `true` in Compose; `false` in Core | Enable Web client sessions |
| `MOERISE_PUBLIC_ORIGIN` | `web.public_origin` | Required when Web is enabled | Public Web origin; scheme, hostname, and port must match the browser URL |
| `MOERISE_TIMEZONE` | `timezone` | `Asia/Hong_Kong` | IANA timezone for reviews and timeline dates |
| `MOERISE_STATE_DIR` | `state_dir` | `/data/state` | Persistent directory containing `state.sqlite3` |
| `MOERISE_INDEX_DIR` | `index_dir` | `/data/index` | Directory containing rebuildable `index.sqlite3` |

### Generate Independent Secrets

Generate each value separately with OpenSSL and copy it into `.env`:

```bash
openssl rand -hex 32     # MOERISE_ACCESS_TOKEN
openssl rand -hex 32     # MOERISE_CURSOR_SECRET
openssl rand -base64 32  # MOERISE_STATE_ENCRYPTION_KEY
```

---

## 2. Docker Compose Deployment

### 1. Bundled Memos 0.31.x

```bash
git clone https://github.com/Fermin214/Moerise.git
cd Moerise
cp .env.example .env

# Start Memos and register the administrator account
docker compose up -d memos
```

Open `http://localhost:5231`, create an account, and obtain its PAT and resource name. Fill in `.env`, then pull the images and start the stack. Core and Web use GHCR images selected by `MOERISE_IMAGE_TAG`, with automatic AMD64 or ARM64 selection:

```bash
docker compose pull moerise web
docker compose up -d
docker compose ps
```

### 2. Connect an Existing Memos Instance

Disable the bundled Memos profile and set the existing instance's origins in `.env`:

```dotenv
COMPOSE_PROFILES=
MOERISE_MEMOS_BASE_URL=https://memos.example.com
MOERISE_MEMOS_PUBLIC_URL=https://memos.example.com
```

Use Memos 0.31.x. The account and PAT must match the resource name in `.env`; `localhost` inside a container refers to that container. Run `docker compose pull moerise web` and `docker compose up -d` to start Moerise.

### 3. HTTPS & Readiness

Web, Core, and Memos bind to loopback by default. For remote access, place an HTTPS reverse proxy in front of Web and set `MOERISE_PUBLIC_ORIGIN` to the browser-facing origin, such as `https://notes.example.com`. Web proxies only `/api/app/v1`; forward machine REST/MCP and Feishu callbacks to Core port 8787 as needed.

```bash
curl --fail http://localhost:3000/health/ready
docker compose logs --tail=100 moerise web
```

Readiness requires readable static assets, a healthy source, a completed full synchronization, and a live background loop; otherwise it returns 503. Inspect synchronization through `GET /api/ext/v1/sync` with a `sync:read` credential.

### 4. Native Linux Binary

Use the binary for your deployment version and a separate configuration, with distinct product-state and index directories:

```bash
./build/moerise check-config --config .local/config.json
./build/moerise serve --config .local/config.json --host 127.0.0.1
```

Build and run Web separately as described in the [Web Guide](WEB.en.md). `--config` takes precedence over `MOERISE_CONFIG_FILE`; when both are omitted, Core reads `.local/config.json`.

---

## 3. Server Resource Profiles

Core calculates computation concurrency and batch budgets from Linux cgroup memory and CPU limits. Merge a profile from `deploy/resources/` into `.env`:

| Server | Profile | Core / Memos / Web Memory Limits | Total Limit |
| --- | --- | --- | --- |
| 1 core / 1 GiB | `deploy/resources/1c1g.env` | 256 / 192 / 96 MiB | 544 MiB |
| 2 cores / 1 GiB | `deploy/resources/2c1g.env` | 384 / 192 / 96 MiB | 672 MiB |
| 2 cores / 2–4 GiB | `deploy/resources/2c2g.env` | 512 / 256 / 128 MiB | 896 MiB |
| 4 cores / 4–8 GiB | `deploy/resources/4c4g.env` | 768 / 256 / 128 MiB | 1,152 MiB |

These values are service memory limits, rather than measured usage or guaranteed note capacities. Note length, chunk count, vector dimensions, and model concurrency affect resource use. Leave room for the operating system and other services; a 2C4G host can start with `2c2g.env`.

Core reads cgroup v1/v2 and parent limits to set its Go memory budget, source page sizes, and computation concurrency; explicit `GOMEMLIMIT` takes precedence. Synchronization, vector construction, and map generation share one background execution slot. Interactive source reads use a separate request queue. Map reads load snapshots, ordinary changes are combined at 24-hour intervals, and opening a note reads the live source.

---

## 4. Backups & Recovery

Moerise separates product state and rebuildable indexes:

```text
/data/
├── state/
│   └── state.sqlite3    # Settings, sessions, reviews, chats: back up
└── index/
    └── index.sqlite3    # Search and map data: rebuildable
```

### 1. Online Backup

Product state can be backed up while the service runs:

```bash
docker compose exec -T moerise moerise backup --output /backups/state-snapshot
mkdir -p .local/backups
docker compose cp moerise:/backups/state-snapshot .local/backups/state-snapshot
```

Use a new output directory for every backup. It contains `state.sqlite3` and `manifest.json`. The backup creates a consistent SQLite snapshot and checks integrity, foreign keys, source binding, migration checksums, and SHA-256. Back up Memos notes, attachments, and accounts separately; the Moerise state snapshot does not contain them.

> [!IMPORTANT]
> Archive `.env`, especially `MOERISE_STATE_ENCRYPTION_KEY`, securely alongside the backup on separate storage. Without the key, restored model and channel credentials cannot be decrypted.

### 2. Restore Product State

Use a compatible version and the same source instance, origin, author, and encryption key. Stop Core before restoring:

```bash
docker compose stop moerise
docker compose run --rm --no-deps -T moerise restore --source /backups/state-snapshot --safety-backup /backups/before-restore
docker compose up -d --no-build moerise
```

Use a new directory for `--safety-backup`, which stores the product state being replaced. Restore validates the snapshot, clears derived indexes and synchronization progress, and revokes old browser sessions. Startup performs a full synchronization, and users sign in again. An ordinary service restart preserves valid sessions.

### 3. Rebuild Indexes

Stop Core first; a product directory allows only one writer. Rebuilding clears derived indexes and synchronization progress, then startup scans Memos and rebuilds. Chats, review plans, settings, and write-idempotency records remain in product state:

```bash
docker compose stop moerise
docker compose run --rm --no-deps -T moerise rebuild-index
docker compose up -d --no-build moerise
```

For native deployments, use `backup --output`, `restore --source --safety-backup`, and `rebuild-index` with the original configuration and data directories; stop the service before restore or rebuild.

## 5. Upgrades & Rollback

1. Back up Memos data, Moerise product state, and configuration. Record the source revision or image digest.
2. Keep the Compose project name, source binding, volumes, and encryption key stable. Set `MOERISE_IMAGE_TAG` in `.env` to the target version, then run `docker compose pull moerise web` and `docker compose up -d`.
3. Check readiness, notes and attachments, settings version, review and chat history, and vector-index status when Embedding is enabled.
4. For rollback, stop the service and switch the code or image. If the database schema is incompatible, restore a state snapshot for the target version before starting it.

Git rollback does not reverse database migrations. Compose pins Memos independently by version and image digest; verify Moerise's protocol support before upgrading Memos.
