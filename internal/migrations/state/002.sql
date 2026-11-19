CREATE TABLE bot_bindings (
    instance_id TEXT NOT NULL REFERENCES bindings(instance_id),
    user_name TEXT NOT NULL,
    kind TEXT NOT NULL,
    bot_id TEXT NOT NULL,
    peer TEXT NOT NULL,
    bound_at INTEGER NOT NULL,
    PRIMARY KEY (instance_id, user_name, kind, bot_id)
);

CREATE TABLE bot_cursors (
    instance_id TEXT NOT NULL REFERENCES bindings(instance_id),
    user_name TEXT NOT NULL,
    kind TEXT NOT NULL,
    bot_id TEXT NOT NULL,
    session TEXT NOT NULL,
    sequence INTEGER NOT NULL,
    PRIMARY KEY (instance_id, user_name, kind, bot_id)
);

CREATE TABLE bot_inbox (
    write_key TEXT PRIMARY KEY,
    instance_id TEXT NOT NULL REFERENCES bindings(instance_id),
    user_name TEXT NOT NULL,
    kind TEXT NOT NULL,
    bot_id TEXT NOT NULL,
    peer TEXT NOT NULL,
    message_id TEXT NOT NULL,
    msg_index TEXT NOT NULL,
    digest TEXT NOT NULL,
    operation TEXT NOT NULL,
    body BLOB,
    received_at INTEGER NOT NULL,
    message_at INTEGER NOT NULL,
    phase TEXT NOT NULL,
    reply TEXT NOT NULL DEFAULT '',
    batch_id TEXT,
    receipt TEXT,
    error_code TEXT,
    available_at INTEGER NOT NULL DEFAULT 0,
    UNIQUE (instance_id, user_name, kind, bot_id, peer, message_id)
);

CREATE INDEX bot_inbox_pending ON bot_inbox (phase, available_at);

CREATE TABLE bot_restore (
    instance_id TEXT PRIMARY KEY REFERENCES bindings(instance_id),
    restored_at INTEGER NOT NULL
);
