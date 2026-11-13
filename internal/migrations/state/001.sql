CREATE TABLE bindings (
    instance_id TEXT PRIMARY KEY,
    base_url TEXT NOT NULL,
    user_name TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE sync_state (
    instance_id TEXT PRIMARY KEY REFERENCES bindings(instance_id),
    watermark TEXT,
    last_incremental TEXT,
    last_full TEXT,
    source_ok INTEGER NOT NULL DEFAULT 0,
    index_generation TEXT,
    upstream_version TEXT,
    last_error TEXT,
    updated_at TEXT NOT NULL
);

CREATE TABLE jobs (
    id TEXT PRIMARY KEY,
    instance_id TEXT NOT NULL REFERENCES bindings(instance_id),
    kind TEXT NOT NULL CHECK (kind IN ('sync')),
    mode TEXT NOT NULL CHECK (mode IN ('incremental', 'full')),
    status TEXT NOT NULL CHECK (status IN ('pending', 'running', 'succeeded')),
    attempts INTEGER NOT NULL DEFAULT 0,
    available_at REAL NOT NULL,
    lease_until REAL,
    created_at TEXT NOT NULL,
    started_at TEXT,
    completed_at TEXT,
    last_error TEXT,
    result_json TEXT
);

CREATE UNIQUE INDEX jobs_pending_unique ON jobs(instance_id, kind) WHERE status = 'pending';

CREATE INDEX jobs_ready ON jobs(status, available_at);

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
    source_created INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (instance_id, user_name, idempotency_key),
    UNIQUE (instance_id, memo_name)
);

CREATE TABLE attachment_writes (
    instance_id TEXT NOT NULL,
    user_name TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    position INTEGER NOT NULL,
    attachment_name TEXT NOT NULL,
    PRIMARY KEY (instance_id, user_name, idempotency_key, position),
    UNIQUE (instance_id, attachment_name),
    FOREIGN KEY (instance_id, user_name, idempotency_key)
        REFERENCES note_writes(instance_id, user_name, idempotency_key)
);

CREATE TABLE review_batches (
    id TEXT PRIMARY KEY,
    instance_id TEXT NOT NULL REFERENCES bindings(instance_id),
    user_name TEXT NOT NULL,
    local_date TEXT NOT NULL,
    timezone TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE (instance_id, user_name, local_date)
);

CREATE TABLE review_items (
    batch_id TEXT NOT NULL REFERENCES review_batches(id),
    position INTEGER NOT NULL,
    memo_name TEXT NOT NULL,
    source_hash TEXT NOT NULL,
    selected_at TEXT NOT NULL,
    PRIMARY KEY (batch_id, memo_name),
    UNIQUE (batch_id, position)
);

CREATE INDEX review_history ON review_items(memo_name, selected_at);

CREATE TABLE review_plans (
    batch_id TEXT PRIMARY KEY REFERENCES review_batches(id),
    instance_id TEXT NOT NULL REFERENCES bindings(instance_id),
    user_name TEXT NOT NULL,
    local_date TEXT NOT NULL,
    settings_version INTEGER NOT NULL,
    rules_json TEXT NOT NULL,
    UNIQUE (instance_id, user_name, local_date)
);

CREATE TABLE review_plan_slots (
    id TEXT PRIMARY KEY,
    batch_id TEXT NOT NULL REFERENCES review_plans(batch_id),
    ordinal INTEGER NOT NULL,
    clock TEXT NOT NULL,
    first_position INTEGER NOT NULL,
    item_count INTEGER NOT NULL,
    UNIQUE (batch_id, clock),
    UNIQUE (batch_id, ordinal)
);

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
    slot_id TEXT REFERENCES review_plan_slots(id),
    UNIQUE (instance_id, user_name, channel_key, logical_key)
);

CREATE INDEX notifications_pending ON notifications(status, available_at);

CREATE INDEX notifications_source_event ON notifications(source_event) WHERE source_event IS NOT NULL;

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

CREATE TABLE review_schedule_slots (
    instance_id TEXT NOT NULL REFERENCES bindings(instance_id),
    user_name TEXT NOT NULL,
    channel_key TEXT NOT NULL,
    timezone TEXT NOT NULL,
    local_date TEXT NOT NULL,
    clock TEXT NOT NULL,
    PRIMARY KEY (instance_id, user_name, channel_key, timezone, local_date, clock)
);

CREATE TABLE review_delivery_parts (
    notification_id TEXT PRIMARY KEY REFERENCES notifications(id) ON DELETE CASCADE,
    root_id TEXT NOT NULL REFERENCES notifications(id),
    position INTEGER NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('text', 'image', 'empty', 'header', 'footer')),
    memo_name TEXT NOT NULL,
    source_version TEXT NOT NULL,
    byte_start INTEGER NOT NULL DEFAULT 0,
    byte_end INTEGER NOT NULL DEFAULT 0,
    attachment_name TEXT NOT NULL DEFAULT '',
    image_key TEXT,
    UNIQUE (root_id, position)
);

CREATE TRIGGER review_delivery_next
AFTER UPDATE OF status ON notifications
WHEN NEW.status = 'sent' AND OLD.status <> 'sent'
BEGIN
    UPDATE notifications SET available_at = 0
    WHERE status = 'pending' AND id IN (
        SELECT next.notification_id FROM review_delivery_parts previous
        JOIN review_delivery_parts next
            ON next.root_id = previous.root_id AND next.position = previous.position + 1
        WHERE previous.notification_id = NEW.id
    );
END;

CREATE TABLE client_sessions (
    session_hash TEXT PRIMARY KEY,
    instance_id TEXT NOT NULL REFERENCES bindings(instance_id),
    user_name TEXT NOT NULL,
    source_binding_hash TEXT NOT NULL,
    refresh_ciphertext BLOB NOT NULL,
    refresh_expires_at INTEGER NOT NULL,
    csrf_token TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0)
);

CREATE INDEX client_sessions_expiry ON client_sessions(refresh_expires_at);

CREATE TABLE product_settings (
    instance_id TEXT PRIMARY KEY REFERENCES bindings(instance_id),
    version INTEGER NOT NULL CHECK (version > 0),
    ciphertext BLOB NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE chat_sessions (
    id TEXT PRIMARY KEY,
    instance_id TEXT NOT NULL REFERENCES bindings(instance_id),
    user_name TEXT NOT NULL,
    creation_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    title TEXT NOT NULL,
    version INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (instance_id, user_name, creation_key)
);

CREATE INDEX chat_sessions_recent ON chat_sessions(instance_id, user_name, updated_at DESC, id DESC);

CREATE TABLE chat_materials (
    chat_id TEXT NOT NULL REFERENCES chat_sessions(id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    memo_name TEXT NOT NULL,
    source_hash TEXT NOT NULL,
    PRIMARY KEY (chat_id, memo_name),
    UNIQUE (chat_id, position)
);

CREATE TABLE chat_turns (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL REFERENCES chat_sessions(id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    request_id TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('insight', 'question')),
    user_content TEXT NOT NULL,
    assistant_content TEXT NOT NULL DEFAULT '',
    message_version INTEGER NOT NULL DEFAULT 1,
    status TEXT NOT NULL CHECK (status IN ('running', 'completed', 'failed', 'canceled', 'interrupted')),
    error_code TEXT NOT NULL DEFAULT '',
    settings_version INTEGER NOT NULL,
    prompt TEXT NOT NULL,
    model TEXT NOT NULL,
    materials_json TEXT NOT NULL,
    usage_json TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (chat_id, request_id),
    UNIQUE (chat_id, position)
);

CREATE UNIQUE INDEX chat_turns_running ON chat_turns(chat_id) WHERE status = 'running';

CREATE TABLE chat_saves (
    turn_id TEXT PRIMARY KEY REFERENCES chat_turns(id) ON DELETE CASCADE,
    message_version INTEGER NOT NULL,
    content_hash TEXT NOT NULL,
    idempotency_key TEXT NOT NULL UNIQUE,
    memo_name TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE notification_rate_limits (
    provider_key TEXT PRIMARY KEY,
    retry_at REAL NOT NULL
);
