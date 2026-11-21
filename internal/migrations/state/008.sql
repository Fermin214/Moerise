CREATE TABLE chat_saves_bak AS SELECT * FROM chat_saves;
DROP TABLE chat_saves;
ALTER TABLE chat_turns RENAME TO chat_turns_old;
CREATE TABLE chat_turns (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL REFERENCES chat_sessions(id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    request_id TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    kind TEXT NOT NULL,
    user_content TEXT NOT NULL,
    assistant_content TEXT NOT NULL DEFAULT '',
    message_version INTEGER NOT NULL DEFAULT 1,
    status TEXT NOT NULL,
    error_code TEXT NOT NULL DEFAULT '',
    settings_version INTEGER NOT NULL,
    prompt TEXT NOT NULL,
    model TEXT NOT NULL,
    materials_json TEXT NOT NULL,
    usage_json TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    material_version INTEGER NOT NULL DEFAULT 1,
    UNIQUE (chat_id, request_id),
    UNIQUE (chat_id, position)
);
INSERT INTO chat_turns (
    id, chat_id, position, request_id, request_hash, kind, user_content,
    assistant_content, message_version, status, error_code, settings_version,
    prompt, model, materials_json, usage_json, created_at, updated_at, material_version
)
SELECT
    id, chat_id, position, request_id, request_hash, kind, user_content,
    assistant_content, message_version, status, error_code, settings_version,
    prompt, model, materials_json, usage_json, created_at, updated_at, material_version
FROM chat_turns_old;
DROP TABLE chat_turns_old;
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
INSERT INTO chat_saves (
    turn_id, message_version, content_hash, idempotency_key, memo_name, created_at, updated_at
)
SELECT turn_id, message_version, content_hash, idempotency_key, memo_name, created_at, updated_at
FROM chat_saves_bak;
DROP TABLE chat_saves_bak;
