# Contributing to Moerise

Issues, pull requests and design discussions are welcome in English or Chinese. A useful bug report explains what you tried, what you expected, what happened, and the relevant Moerise and Memos versions. Remove credentials and private note content from examples.

For a substantial feature, describe the use case in an issue first so the design can be discussed. For a fix, include enough detail to reproduce the problem and explain how you checked the result.

## Development setup

Use Linux or Linux containers, Go 1.27+, Node.js 24+ and npm. Docker Engine is needed for real Memos integration tests. Ordinary unit tests and the Web build do not need Docker, a running Memos instance or model credentials.

```bash
go mod download
npm --prefix web ci
npm --prefix web run build
go test ./...
npm --prefix web test
```

For a full local stack, follow [Getting started](docs/GETTING-STARTED.md). To run Core from source, copy `config.example.json` to `.local/config.json`, configure a separate test Memos instance, and use separate `.local/state` and `.local/index` directories. Configure `web.enabled`, `web.public_origin` and a state encryption key for browser access. Check the field names in the template.

```bash
go run . check-config
go run . serve --host 127.0.0.1
npm --prefix web run build
npm --prefix web start
```

Core defaults to port 8787 and Web to port 3000. Web's `PUBLIC_ORIGIN` must match Core's configured public origin; `CORE_ORIGIN` selects Web's fixed upstream. Web serves `web/dist`, so rebuild after source edits. See the [Chinese development guide](docs/DEVELOPMENT.md) for integration scenarios and optional provider checks.

## Find your way around

| Path | Purpose |
| --- | --- |
| `main.go`, `internal/` | CLI, Go services, HTTP APIs and platform adapters |
| `internal/openapi.json` | Embedded API contract |
| `internal/migrations/` | Versioned SQLite migrations |
| `web/src/` | Browser UI: JavaScript, HTML and CSS, without runtime packages |
| `web/server.mjs`, `web/build.mjs` | Same-origin proxy, static serving and Web build |
| `web/test/` | Node unit tests and Playwright browser suites |
| `tests/` | Integration tests against real Memos |
| `scripts/` | Joint acceptance flows and performance tools |
| `docs/` | Product, API, architecture and operations documentation |

Memos is the authority for notes, tags, attachments and accounts. Access it through official APIs and respect the bound author, including when an administrator PAT is used. `state.sqlite3` contains product state that needs backups; `index.sqlite3` is rebuildable. External REST/MCP access is read-only.

Add a new numbered SQL migration for schema changes; do not rewrite applied migrations. Update API definitions and documentation together. Keep credentials and private source material out of logs, repository files and browser storage.

## Check a change

Choose existing checks that exercise the behavior you changed:

```bash
go vet ./...
gofmt -l main.go internal tests
go test ./...
npm --prefix web run build
npm --prefix web test
cd web && npx playwright install chromium && npm run test:browser
```

For source synchronization, Memos protocol or backup changes, run `go test -tags=integration ./tests -v -count=1` from the repository root with Docker available. CI also checks a build without cgo, Go races and dependencies for known vulnerabilities. The race detector needs a C toolchain.

Use end-to-end checks for user behavior. Add focused tests for important boundaries or failure paths that those flows cannot reliably exercise. Documentation changes need factual and link checks.

## Work on translations

English and Simplified Chinese are supported. The translation layer is in `web/src/i18n.js`; Chinese source phrases serve as message keys, and `web/src/en.js` provides English messages. Missing translations fall back to the source phrase. An unsupported browser language falls back to English.

- Use `l('中文消息', { name: value })` for a deferred message and bind it with `setText` or `setAttr`. Named or numbered placeholders let translators change word order. English messages can use a function for singular/plural wording.
- Annotate static captions with `data-i18n`; use `data-i18n-placeholder`, `data-i18n-aria-label` and similar attributes for accessible names and hints. Preserve icons and keyboard shortcuts.
- `setText` creates a bound caption; append icons or keyboard shortcuts after binding, or use `data-i18n` on static markup. Avoid constructing translated text with template interpolation; bind a complete message with placeholders instead. Use `locale()` with `Intl.DateTimeFormat` for dates.
- Translate interface text, rather than note bodies, tags, filenames, custom prompts or conversation history. New language support should preserve these boundaries.

The browser stores only the explicit language preference under `moerise.language`. Switching language updates text in place and preserves drafts, secret inputs, selections, map position and active chat streams. A generation request freezes its reply language along with its request ID, so a retry remains the same request after a language switch. An unchanged built-in insight prompt uses an English equivalent for English requests; custom prompts are used verbatim.

Check English and Chinese on desktop and narrow screens. The browser suites pin Chinese fixtures explicitly and include English workflows. Confirm accessible labels, dates, plural forms, errors and private-data handling as well as visible captions.

## License

Contributions are distributed under the project's [MIT license](LICENSE). Keep applicable third-party notices with reused code and assets.
