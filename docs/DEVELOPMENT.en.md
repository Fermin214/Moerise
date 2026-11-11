# Moerise Development Guide

[简体中文](DEVELOPMENT.md)

This guide covers local development, test suites, end-to-end integration, and evaluation workflows for Moerise contributors.

---

## 1. Environment

- **Go 1.27+**: Server builds, tests, and static analysis. The pure Go `modernc.org/sqlite` driver needs no C compiler; `go test -race` requires a C toolchain.
- **Node.js 24+ and npm**: Web builds and tests.
- **Docker Engine and Compose 2.24+**: Integration tests against real Memos containers. Ordinary unit tests do not require Docker.
- **Git**: Version control.

---

## 2. Local Development

### 1. Dependencies

```bash
go mod download
go mod verify
```

### 2. Run Core

1. Copy the configuration template: `cp config.example.json .local/config.json`.
2. Configure a test Memos origin, PAT, author, and independent `access_tokens` and `cursor_secret`.
3. Set `state_dir` and `index_dir` to `.local/state` and `.local/index`.
4. Enable `web.enabled`, set `web.public_origin` to `http://localhost:3000`, and generate an independent `web.state_encryption_key` with `openssl rand -base64 32`.
5. Validate and start Core:

   ```bash
   go run . check-config
   go run . serve --host 127.0.0.1
   ```

### 3. Run Web

In another terminal, build and start the static server and proxy:

```bash
npm --prefix web ci
npm --prefix web run build
CORE_ORIGIN=http://127.0.0.1:8787 PUBLIC_ORIGIN=http://localhost:3000 npm --prefix web start
```

Open `http://localhost:3000`. Rebuild with `npm --prefix web run build` after editing frontend source.

---

## 3. Code Checks & Unit Tests

### 1. Formatting, Analysis & Tests

Run the checks relevant to the change:

```bash
# Formatting: no output means files are formatted
gofmt -l main.go internal tests

# Go static analysis and unit tests
go vet ./...
go test ./...

# Web build and unit tests
npm --prefix web run build
npm --prefix web test
```

### 2. Race Detector

With a C toolchain available:

```bash
go test -race ./...
```

---

## 4. Playwright Browser Tests

Playwright exercises real Web interactions:

```bash
cd web
npx playwright install chromium
npm run test:browser
```

Install missing system libraries as directed by Playwright. CI selects the Runner's preinstalled Chrome with `CHROMIUM_PATH`; local runs can also set this variable to a compatible Chrome or Chromium executable.

### Browser Suites

- `web/test/browser.mjs`: Core interactions, Markdown, inline editing, and combined filters.
- `web/test/settings-browser.mjs`: Settings persistence, encrypted credentials, version conflicts, and timezone updates.
- `web/test/discovery-browser.mjs`: Full-day review plans, walk progression, and interrupted-request retries.
- `web/test/map-browser.mjs`: Map rendering, pointer and keyboard interactions, and live note reading.
- `web/test/insights-browser.mjs`: Material selection, SSE replies, follow-up questions, and saving replies.

---

## 5. Real Memos Integration

Run container integration tests for changes affecting Memos synchronization, migrations, backup/recovery, or notification delivery:

```bash
go test -tags=integration ./tests -v -count=1
```

The suite pulls the official Memos 0.31.x image, initializes an isolated container, registers an account and PAT, exercises API reads and writes and failure recovery, then cleans up its temporary containers and data.

---

## 6. Benchmarks & Retrieval Evaluation

### 1. Vector Scanning & Map Layout

```bash
# Vector dot products and memory scans
go test ./internal -run '^$' -bench 'Benchmark(VectorIndex|Semantic)' -benchmem

# 2D PCA layout
go test ./internal -run '^$' -bench '^BenchmarkMapLayout$' -benchmem
```

### 2. Retrieval Quality

`scripts/evaluate-search.py` measures Hit@5, MRR@5, and query latency against labeled questions:

```bash
python3 scripts/evaluate-search.py --config .local/config.json --credential reader --questions .local/search-questions.json
```

First run Core with synchronization and vector construction complete. Configure a credential named `reader` with `notes:read`. The question file is a JSON array with `query` and an `expected` array of resource names, optionally `state` and `tags`. Output defaults to `.local/search-evaluation.json`; choose a new output filename for each run. Vector benchmarks measure computation alone, while complete API latency includes source authentication, model requests, and live-text verification.

---

## 7. Contribution Guidelines

1. **API contracts**: Update `internal/openapi.json` and [API Documentation](API-V1.en.md) when changing `internal/models.go` or `internal/client_models.go`.
2. **Migrations**: Add a new numbered SQL file in `internal/migrations/` for schema changes. Never rewrite applied migrations.
3. **Private data**: Keep production tokens, real credentials, and private notes out of tests, scripts, and documentation.
