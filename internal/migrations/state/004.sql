CREATE TABLE review_batches (
    id TEXT PRIMARY KEY,
    instance_id TEXT NOT NULL REFERENCES bindings(instance_id),
    user_name TEXT NOT NULL,
    local_date TEXT NOT NULL,
    timezone TEXT NOT NULL,
    rule_version TEXT NOT NULL,
    sequence INTEGER NOT NULL,
    request_key TEXT,
    created_at TEXT NOT NULL,
    UNIQUE (instance_id, user_name, local_date, timezone, rule_version, sequence),
    UNIQUE (instance_id, user_name, local_date, timezone, rule_version, request_key)
);

CREATE TABLE review_items (
    batch_id TEXT NOT NULL REFERENCES review_batches(id),
    position INTEGER NOT NULL,
    memo_name TEXT NOT NULL,
    source_hash TEXT NOT NULL,
    selected_at TEXT NOT NULL,
    opened_at TEXT,
    skipped_at TEXT,
    PRIMARY KEY (batch_id, memo_name),
    UNIQUE (batch_id, position)
);

CREATE INDEX review_history ON review_items(memo_name, selected_at);
