# Moerise Search & Random Walk

[简体中文](SEARCH.md)

The shared `SearchService` provides keyword, vector-semantic, and hybrid retrieval for Web, machine REST, and read-only MCP.

Web's `keyword` mode reads live filters through the official Memos list API and orders by creation time. Machine keyword search and Web enhanced search use the indexes below. Machine search defaults to `hybrid`; Web defaults to `keyword`.

---

## 1. Search Modes

| Mode | Technique | Embedding Required | Behavior |
| --- | --- | --- | --- |
| `keyword` | FTS5 with Chinese pre-tokenization for indexed search | No | Match Chinese phrases, English words, and tags |
| `semantic` | Vector dot products and cosine similarity | Yes | Find related meaning across different wording |
| `hybrid` | FTS5, vectors, and RRF | Optional | Combine exact and semantic signals; fall back to keywords and tags when vectors are unavailable |

---

## 2. Retrieval & Ranking

### 1. Chinese FTS5

- **Tokenization**: Continuous Chinese text yields characters and adjacent bigrams. English and numeric words are lowercased without stemming.
- **Chunk boundaries**: Adjacent Chinese bigrams crossing a boundary are indexed on both sides to preserve short-query recall.
- **Query safety**: Queries become safely quoted FTS expressions rather than raw SQL or FTS syntax.

### 2. Vector Scanning

- Cache the active generation as a `float32` matrix in memory rather than rereading it for every query.
- Use exact cosine retrieval with computation shards bounded by effective CPU count; scanning costs are distinct from complete request latency.

### 3. Reciprocal Rank Fusion

Keyword, tag, and semantic channels each contribute up to 100 candidate chunks:

```math
\operatorname{RRF}(d) = \sum_{m \in \{\mathrm{keyword},\mathrm{tag},\mathrm{semantic}\}} \frac{1}{60 + \operatorname{rank}_m(d)}
```

- A note takes its highest chunk score.
- Results include matching source locations and `keyword`, `semantic`, or `tag` signals.

---

## 3. Source Verification

Before returning results:

1. Verify the bound Memos author.
2. Fetch candidate notes through official APIs.
3. Check existence, visibility, author, and source version.
4. Remove deleted, out-of-scope, or filter-mismatched candidates and repair their projection. Source failures fail the request without returning cached text.

---

## 4. Random Walk

Random Walk explores notes through semantic connections:

1. **Seed**: Start from a specific note or a random note.
2. **Candidates**: Compare document vectors to form strong and weaker semantic candidate pools.
3. **Sampling**: Choose the next note probabilistically without a fixed tag preference.
4. **Path deduplication**: The caller submits the visited path; the server excludes those resource names without storing cross-entry reading history.
5. **Boundary**: When no eligible unvisited candidate remains, the walk reports its end and can be restarted.

Both random starts and semantic steps require enabled Embedding and a ready index. Related-note retrieval can combine keywords, tags, and existing document vectors without additional model requests.
