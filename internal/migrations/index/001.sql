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
    generation TEXT NOT NULL
);
