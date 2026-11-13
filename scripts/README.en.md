# Moerise Development Scripts

[简体中文](README.md)

Run all commands from the repository root. Scripts are grouped into joint acceptance, performance evaluation, and shared utilities. See [Development](../docs/DEVELOPMENT.en.md) for basic tests.

---

## 1. Joint Acceptance

`acceptance/` connects real Memos, Core, Web, and browsers to exercise cross-service behavior. It requires Docker Engine, Compose 2.24+, Go 1.27+, Node.js 24+, and an installed Playwright browser.

```bash
npm --prefix web ci
npm --prefix web exec -- playwright install chromium
export MOERISE_PLAYWRIGHT_MODULE="$PWD/web/node_modules/playwright/index.mjs"
node scripts/acceptance/check-compose.mjs --browser
```

| Script | Coverage |
| --- | --- |
| `check-compose.mjs` | Compose build, startup, health, proxy, sessions, persistence, and backup/restore; `--browser` adds browser flows |
| `check-discovery.mjs` | Daily reviews, semantic walks, read-only machine access, and model failures |
| `check-chat.mjs` | Material selection, streaming replies, follow-ups, saving notes, and restart recovery |
| `check-models.mjs` | Settings changes, diagnostics, LLM/embedding protocols, and failure responses |
| `check-map.mjs` | Real map points, browser interaction, snapshots, source changes, and backup/restore |

All joint scripts except Compose's optional browser mode require `MOERISE_PLAYWRIGHT_MODULE`. Set `MOERISE_PLAYWRIGHT_CHANNEL=chrome` to use installed Chrome. The map script requires Linux; `MOERISE_MAP_SCALE` accepts 100–10000, default 1000.

Scripts create isolated containers, test accounts, and fictional notes. Local model substitutes avoid real provider credentials. Reports and screenshots go into separate randomized directories under `.local/`; scripts clean up the services and test data they create. The Compose script supports `--help`, `--keep` to retain a successful test environment, and `--skip-build` to reuse built images.

---

## 2. Performance and Search Quality

`performance/benchmark-search.py` generates 10000 synthetic notes with 1024-dimensional vectors in disposable databases. It measures index construction, cold/warm vector scans, and the search service.

```bash
python3 scripts/performance/benchmark-search.py --cpu 2 --output .local/search-2c.json
python3 scripts/performance/benchmark-search.py --cpu 4 --output .local/search-4c.json
```

Linux can use native Go; other environments can select a Linux Go image with `--docker-image`. `--cpu` sets the measurement process's `GOMAXPROCS`; it does not impose a host CPU quota. Use Docker CPU and memory limits when testing container quotas. Select individual stages with `--phase`; see `--help` for all options.

`performance/evaluate-search.py` submits labeled questions to a running Core and reports Hit@5, MRR@5, and query latency.

```bash
python3 scripts/performance/evaluate-search.py \
  --config .local/config.json --credential reader \
  --questions .local/search-questions.json \
  --output .local/search-evaluation.json
```

Core must complete sync; semantic search also requires a completed embedding index. The questions file is a JSON array with `query` and `expected` per item, plus optional `state` and `tags`. `expected` is an array of note resource names. Both scripts require a new output filename.

---

## 3. Shared Utilities and Data

`lib/process.mjs` runs child processes with timeouts, cancellation, and output limits. Its tests cover exit handling and cleanup after timeout or cancellation.

```bash
node --test scripts/lib/*.test.mjs
```

`tests/testdata/search-quality.json` defines retrieval labels against public repository documentation; `chat-quality.json` provides fictional chat evaluation materials. See [Development](../docs/DEVELOPMENT.en.md) for optional real-provider runs.
