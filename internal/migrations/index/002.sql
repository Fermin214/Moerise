ALTER TABLE projection_meta ADD COLUMN revision INTEGER NOT NULL DEFAULT 0;

CREATE TABLE vector_generations (
    id TEXT PRIMARY KEY,
    instance_id TEXT NOT NULL,
    user_name TEXT NOT NULL,
    space_hash TEXT NOT NULL,
    provider TEXT NOT NULL,
    protocol TEXT NOT NULL,
    base_url TEXT NOT NULL,
    model TEXT NOT NULL,
    dimensions INTEGER NOT NULL,
    chunk_version TEXT NOT NULL,
    projection_revision INTEGER NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('building', 'active')),
    last_error TEXT,
    created_at TEXT NOT NULL,
    published_at TEXT
);
CREATE UNIQUE INDEX vector_generation_status ON vector_generations(instance_id, user_name, status);

CREATE TABLE vector_notes (
    generation_id TEXT NOT NULL REFERENCES vector_generations(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    source_hash TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    PRIMARY KEY (generation_id, name)
);

CREATE TABLE vector_chunks (
    generation_id TEXT NOT NULL,
    name TEXT NOT NULL,
    ordinal INTEGER NOT NULL,
    byte_start INTEGER NOT NULL,
    byte_end INTEGER NOT NULL,
    line_start INTEGER NOT NULL,
    line_end INTEGER NOT NULL,
    content_hash TEXT NOT NULL,
    content TEXT NOT NULL,
    vector BLOB,
    PRIMARY KEY (generation_id, name, ordinal),
    FOREIGN KEY (generation_id, name) REFERENCES vector_notes(generation_id, name) ON DELETE CASCADE
);
CREATE INDEX vector_chunks_pending ON vector_chunks(generation_id, vector IS NULL, name, ordinal);
CREATE INDEX vector_chunks_content ON vector_chunks(content_hash, generation_id);

CREATE TABLE vector_cache (
    space_hash TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    vector BLOB NOT NULL,
    PRIMARY KEY (space_hash, content_hash)
);
