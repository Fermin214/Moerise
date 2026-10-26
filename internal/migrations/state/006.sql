CREATE TABLE notification_schedules (
    instance_id TEXT NOT NULL REFERENCES bindings(instance_id),
    user_name TEXT NOT NULL,
    channel_key TEXT NOT NULL,
    timezone TEXT NOT NULL,
    last_date TEXT NOT NULL,
    PRIMARY KEY (instance_id, user_name, channel_key, timezone)
);

CREATE TABLE notifications (
    id TEXT PRIMARY KEY,
    instance_id TEXT NOT NULL REFERENCES bindings(instance_id),
    user_name TEXT NOT NULL,
    channel_key TEXT NOT NULL,
    logical_key TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('review', 'reply')),
    local_date TEXT NOT NULL,
    batch_id TEXT REFERENCES review_batches(id),
    source_event TEXT REFERENCES channel_events(message_id),
    status TEXT NOT NULL CHECK (status IN ('pending', 'running', 'unknown', 'sent')),
    platform_uuid TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    available_at REAL NOT NULL,
    receipt TEXT,
    last_error TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (instance_id, user_name, channel_key, logical_key)
);
CREATE INDEX notifications_pending ON notifications(status, available_at);

CREATE TABLE notification_attempts (
    id TEXT PRIMARY KEY,
    notification_id TEXT NOT NULL REFERENCES notifications(id),
    attempt INTEGER NOT NULL,
    platform_uuid TEXT NOT NULL,
    started_at TEXT NOT NULL,
    completed_at TEXT,
    outcome TEXT NOT NULL,
    receipt TEXT,
    error_code TEXT,
    UNIQUE (notification_id, attempt)
);

CREATE TABLE notification_resolutions (
    id TEXT PRIMARY KEY,
    notification_id TEXT NOT NULL REFERENCES notifications(id),
    decision TEXT NOT NULL,
    receipt TEXT,
    resolved_by TEXT NOT NULL,
    created_at TEXT NOT NULL
);
