# Moerise Web Client Guide

[简体中文](WEB.md)

Moerise Web is an independently built browser client using vanilla JavaScript, HTML, and CSS. It has no third-party runtime framework dependencies.

---

## 1. Layout & Interaction

1. **Two-column layout**:
   - **Sidebar (260px)**: Timeline, Daily Review, Random Walk, AI Insights, Cognitive Map, search, tags, calendar filters, and settings.
   - **Main area (maximum 760px)**: Centered reading layout that adapts to smaller screens.
2. **Markdown notes**:
   - The top composer accepts Markdown, native `#tags`, images, and file attachments. Saved notes render as Markdown.
   - `Ctrl+Enter` or `Cmd+Enter` saves; `Esc` cancels.
3. **Inline editing**:
   - Select Edit from a note's More menu to edit in place. Saving preserves its creation-time position.

---

## 2. Internationalization (i18n)

Web supports **English and Simplified Chinese**:

1. **Detection and selection**:
   - The first visit reads `navigator.language`; non-Chinese languages fall back to English.
   - A language selector appears on the login page and at the bottom of the sidebar.
2. **In-place updates**:
   - The explicit preference is stored under `moerise.language`.
   - Switching updates bound DOM text and accessible labels without refreshing the page, preserving drafts, materials, map position, and active chat streams.
3. **Original content**:
   - Translation covers interface controls and system messages. Notes, tags, filenames, custom prompts, and historical conversations retain their original text.

---

## 3. 2D Cognitive Map

The map presents its layout with focusable DOM markers and CSS transforms:

- **Pointer gestures**: Drag to pan, use the wheel to zoom, or pinch on touch devices.
- **Keyboard controls**:
  - Arrow keys: Pan.
  - `+` / `-`: Zoom.
  - `Home`: Fit the full map.
  - `R`: Reread the latest published snapshot.
  - `Tab` / `Shift+Tab`: Move between markers; `Enter` opens a note.
- **Live reading**: Select a point to read its note; the detail view retrieves live Memos text and attachments. Returning restores the map position.

---

## 4. Build & Run

Web requires Node.js 24+ and has no third-party runtime dependencies:

```bash
npm --prefix web ci
npm --prefix web run build
npm --prefix web start
```

### Build Pipeline

- `build.mjs` compiles static assets and combines CSS into `web/dist/`.
- Content-hashed asset names use `max-age=31536000, immutable` caching.
- Brotli (`.br`) and Gzip (`.gz`) files are generated during the build and selected by the server.

---

## 5. Environment & Same-Origin Proxy

The Web server forwards `/api/app/v1/*` to a fixed Core origin:

| Variable | Default | Description |
| --- | --- | --- |
| `CORE_ORIGIN` | `http://127.0.0.1:8787` | Core origin, used only by the server |
| `PUBLIC_ORIGIN` | `http://localhost:3000` | Browser-facing origin; remote production origins require HTTPS |
| `HOST` | `0.0.0.0` | Web listen address |
| `PORT` | `3000` | Web listen port |

---

## 6. Health Checks

| Path | Methods | Success | Check |
| --- | --- | --- | --- |
| `/health/live` | GET, HEAD | 200 `{"status":"ok"}` | `dist/index.html` is readable |
| `/health/ready` | GET, HEAD | 200 `{"status":"ready"}` | Static assets are readable and Core `/health/ready` succeeds |
