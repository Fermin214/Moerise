CREATE TABLE note_writes (
    instance_id TEXT NOT NULL REFERENCES bindings(instance_id),
    user_name TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    memo_name TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'unknown', 'succeeded')),
    last_error TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (instance_id, user_name, idempotency_key),
    UNIQUE (instance_id, memo_name)
);
