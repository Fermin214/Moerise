CREATE VIRTUAL TABLE memo_fts USING fts5(instance_id UNINDEXED, user_name UNINDEXED, name UNINDEXED, content, tokenize = 'trigram');
INSERT INTO memo_fts SELECT instance_id, user_name, name, json_extract(payload_json, '$.content') FROM memos;
CREATE TRIGGER memos_search_insert AFTER INSERT ON memos BEGIN
    INSERT INTO memo_fts VALUES (new.instance_id, new.user_name, new.name, json_extract(new.payload_json, '$.content'));
END;
CREATE TRIGGER memos_search_update AFTER UPDATE ON memos BEGIN
    DELETE FROM memo_fts WHERE instance_id = old.instance_id AND user_name = old.user_name AND name = old.name;
    INSERT INTO memo_fts VALUES (new.instance_id, new.user_name, new.name, json_extract(new.payload_json, '$.content'));
END;
CREATE TRIGGER memos_search_delete AFTER DELETE ON memos BEGIN
    DELETE FROM memo_fts WHERE instance_id = old.instance_id AND user_name = old.user_name AND name = old.name;
END;

CREATE TABLE embedding_spaces (
    id TEXT PRIMARY KEY,
    model TEXT NOT NULL,
    dimensions INTEGER NOT NULL DEFAULT 0,
    chunk_version TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('building', 'active', 'retired')),
    revision INTEGER NOT NULL DEFAULT 0,
    last_error TEXT,
    updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX one_active_embedding ON embedding_spaces(status) WHERE status = 'active';
CREATE TABLE embedding_memos (
    space_id TEXT NOT NULL REFERENCES embedding_spaces(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    PRIMARY KEY (space_id, name)
);
CREATE TABLE embedding_chunks (
    space_id TEXT NOT NULL,
    name TEXT NOT NULL,
    position INTEGER NOT NULL,
    start_rune INTEGER NOT NULL,
    end_rune INTEGER NOT NULL,
    content_hash TEXT NOT NULL,
    vector BLOB NOT NULL,
    PRIMARY KEY (space_id, name, position),
    FOREIGN KEY (space_id, name) REFERENCES embedding_memos(space_id, name) ON DELETE CASCADE
);
