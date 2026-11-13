# Contributing to Moerise

[简体中文](CONTRIBUTING.zh-CN.md)

We welcome issues, pull requests, and architecture discussions in English or Simplified Chinese.

When reporting bugs, please provide a clear reproduction: explain what you attempted, the expected outcome, the actual result, and the relevant Moerise and Memos versions. Never include private credentials or confidential note content in issues or pull requests.

For major feature proposals, open an issue first to discuss the requirements and design before writing code. For bug fixes, include tests or clear verification steps demonstrating the fix.

---

## Development Setup

The development environment requires Linux or Linux containers, Go 1.27+, Node.js 24+, and npm. Docker Engine is required for integration tests running against a real Memos container. Standard unit tests and Web frontend builds do not require Docker, a live Memos instance, or third-party model credentials.

```bash
# Download Go dependencies
go mod download

# Install frontend dependencies and build
npm --prefix web ci
npm --prefix web run build

# Run Go unit tests and frontend unit tests
go test ./...
npm --prefix web test
```

### Running from Source

To run Moerise Core from source:

1. Copy `config.example.json` to `.local/config.json`.
2. Configure a dedicated test Memos instance and set isolated directories for `.local/state` and `.local/index`.
3. Set `web.enabled` to `true`, configure `web.public_origin`, and generate a 32-byte base64 state encryption key.
4. Start Core and the Web dev server:

```bash
go run . check-config
go run . serve --host 127.0.0.1
npm --prefix web run build
npm --prefix web start
```

By default, Moerise Core listens on port `8787` and Web listens on port `3000`. Web's `PUBLIC_ORIGIN` must match Core's `web.public_origin`. Web forwards `/api/app/v1` calls to `CORE_ORIGIN` (defaults to `http://127.0.0.1:8787`). Web serves compiled assets from `web/dist`, so rebuild the frontend after editing source files. See [Development Guide](docs/DEVELOPMENT.en.md) for full details on integration suites and test scripts.

---

## Codebase Organization

See the [Codebase Guide](docs/CODEBASE.en.md) for module groups and reading order.

| Path | Purpose |
| --- | --- |
| `main.go`, `internal/` | CLI commands, Go background services, HTTP APIs, and platform adapters |
| `internal/openapi.json` | Embedded OpenAPI v3 specification |
| `internal/migrations/` | Versioned SQLite migrations for `state.sqlite3` and `index.sqlite3` |
| `web/src/` | Lightweight vanilla JavaScript/HTML/CSS frontend (zero runtime dependencies) |
| `web/server.mjs`, `web/build.mjs` | Static file server, same-origin proxy, and asset build scripts |
| `web/test/` | Node.js unit tests and Playwright browser integration tests |
| `tests/` | End-to-end integration tests using live Memos containers |
| `scripts/` | Joint acceptance, performance evaluation, and shared utilities; see the [script guide](scripts/README.en.md) |
| `docs/` | Comprehensive technical, operational, and API documentation |

### Key Development Guidelines

1. **Memos Authority**: Memos remains the single source of truth for notes, tags, media attachments, and authentication. Always interact with Memos through its official APIs, respecting the author boundaries bound to the instance.
2. **Dual-Database Invariant**: `state.sqlite3` holds irreplaceable application state (settings, chat sessions, review history); `index.sqlite3` holds disposable indexes that must be rebuildable from Memos at any time.
3. **Database Migrations**: Add numbered SQL files in `internal/migrations/` for schema changes. Never modify an existing, committed migration.
4. **Read-Only External Access**: External machine interfaces (`/api/ext/v1` and `/mcp`) are strictly read-only. Note writes use the owner's authenticated client session or internal dedicated capture services.

---

## Verifying Changes

Before submitting pull requests, run the test suites relevant to your modifications:

```bash
# Code formatting and static analysis
go vet ./...
gofmt -l main.go internal tests

# Go unit tests
go test ./...

# Frontend build and unit tests
npm --prefix web run build
npm --prefix web test

# Playwright browser end-to-end tests (requires Chromium)
cd web && npx playwright install chromium && npm run test:browser
```

For changes affecting Memos synchronization, protocol handling, or backups, run the integration test suite with Docker running:

```bash
go test -tags=integration ./tests -v -count=1
```

---

## Working with Translations

Moerise supports English and Simplified Chinese. The translation layer resides in `web/src/i18n.js`:
- Chinese source phrases serve as message keys; `web/src/en.js` maps keys to English messages.
- Missing translations automatically fall back to the source Chinese string.
- If the browser language is neither Chinese nor English, the UI defaults to English.

### Internationalization Guidelines

- Use `l('中文文案', { name: value })` for parameterized messages bound via `setText` or `setAttr`. Use named placeholders so translators can adapt word order.
- Annotate static elements in HTML templates with `data-i18n`. Use `data-i18n-placeholder` or `data-i18n-aria-label` for accessibility attributes.
- Avoid dynamic string concatenation. Always use full message keys with placeholders.
- Translate interface controls and notifications only. Never translate user note bodies, tags, filenames, custom prompts, or chat conversation records.

### Documentation Translations

Public documentation uses separate English and Chinese files, listed in the [Documentation Index](docs/README.md). Update both versions when behavior, parameters, or commands change. Follow the existing structure and writing style, and preserve variable names, API paths, protocol fields, and code identifiers.

---

## License

All contributions are licensed under the project's [MIT License](LICENSE).
