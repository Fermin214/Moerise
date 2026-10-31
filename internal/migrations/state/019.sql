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
