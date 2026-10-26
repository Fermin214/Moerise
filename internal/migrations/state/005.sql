CREATE TABLE channel_events (
    message_id TEXT PRIMARY KEY,
    event_id TEXT NOT NULL UNIQUE,
    event_hash TEXT NOT NULL,
    app_id TEXT NOT NULL,
    sender_open_id TEXT NOT NULL,
    payload BLOB,
    status TEXT NOT NULL CHECK (status IN ('pending', 'processing', 'processed', 'succeeded')),
    result_kind TEXT,
    resource_name TEXT,
    reply_uuid TEXT NOT NULL UNIQUE,
    reply_status TEXT NOT NULL DEFAULT 'pending',
    receipt TEXT,
    attempts INTEGER NOT NULL DEFAULT 0,
    available_at REAL NOT NULL,
    created_at TEXT NOT NULL,
    last_error TEXT
);
CREATE INDEX channel_events_pending ON channel_events(status, available_at);
