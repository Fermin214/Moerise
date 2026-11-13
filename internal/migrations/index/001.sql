CREATE TABLE memos (
    instance_id TEXT NOT NULL,
    user_name TEXT NOT NULL,
    name TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('NORMAL', 'ARCHIVED')),
    update_time TEXT NOT NULL,
    source_hash TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    PRIMARY KEY (instance_id, user_name, name)
);

CREATE INDEX memos_page ON memos(instance_id, user_name, state, update_time DESC, name DESC);

CREATE TABLE scan_memos (
    run_id TEXT NOT NULL,
    name TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    PRIMARY KEY (run_id, name)
);

CREATE TABLE projection_meta (
    instance_id TEXT PRIMARY KEY,
    generation TEXT NOT NULL,
    revision INTEGER NOT NULL DEFAULT 0
);

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

CREATE TABLE search_notes (
    instance_id TEXT NOT NULL,
    user_name TEXT NOT NULL,
    name TEXT NOT NULL,
    source_hash TEXT NOT NULL,
    PRIMARY KEY (instance_id, user_name, name),
    FOREIGN KEY (instance_id, user_name, name)
        REFERENCES memos(instance_id, user_name, name) ON DELETE CASCADE
);

CREATE TABLE search_chunks (
    id INTEGER PRIMARY KEY,
    instance_id TEXT NOT NULL,
    user_name TEXT NOT NULL,
    name TEXT NOT NULL,
    ordinal INTEGER NOT NULL,
    byte_start INTEGER NOT NULL,
    byte_end INTEGER NOT NULL,
    line_start INTEGER NOT NULL,
    line_end INTEGER NOT NULL,
    content TEXT NOT NULL,
    tokens TEXT NOT NULL,
    UNIQUE (instance_id, user_name, name, ordinal),
    FOREIGN KEY (instance_id, user_name, name)
        REFERENCES search_notes(instance_id, user_name, name) ON DELETE CASCADE
);

CREATE VIRTUAL TABLE search_fts USING fts5(tokens, content='search_chunks', content_rowid='id');

CREATE TRIGGER search_chunks_insert AFTER INSERT ON search_chunks BEGIN
    INSERT INTO search_fts(rowid, tokens) VALUES (new.id, new.tokens);
END;

CREATE TRIGGER search_chunks_delete AFTER DELETE ON search_chunks BEGIN
    INSERT INTO search_fts(search_fts, rowid, tokens) VALUES ('delete', old.id, old.tokens);
END;

CREATE TABLE map_layouts (
    instance_id TEXT NOT NULL,
    user_name TEXT NOT NULL,
    space_hash TEXT NOT NULL,
    algorithm TEXT NOT NULL,
    generation_id TEXT NOT NULL,
    projection_revision INTEGER NOT NULL,
    version TEXT NOT NULL,
    basis_json TEXT NOT NULL,
    generated_at TEXT NOT NULL,
    PRIMARY KEY (instance_id, user_name)
);

CREATE TABLE map_points (
    instance_id TEXT NOT NULL,
    user_name TEXT NOT NULL,
    name TEXT NOT NULL,
    source_hash TEXT NOT NULL,
    vector_hash TEXT NOT NULL,
    x REAL NOT NULL,
    y REAL NOT NULL,
    semantic INTEGER NOT NULL CHECK (semantic IN (0, 1)),
    tags_json TEXT NOT NULL DEFAULT '[]',
    PRIMARY KEY (instance_id, user_name, name),
    FOREIGN KEY (instance_id, user_name)
        REFERENCES map_layouts(instance_id, user_name) ON DELETE CASCADE
);
