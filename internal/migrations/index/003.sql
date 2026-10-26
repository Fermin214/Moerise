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
