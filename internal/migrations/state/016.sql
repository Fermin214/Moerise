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
