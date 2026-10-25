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
CREATE TABLE product_state (
    instance_id TEXT NOT NULL REFERENCES bindings(instance_id),
    namespace TEXT NOT NULL,
    key TEXT NOT NULL,
    value_json TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (instance_id, namespace, key)
);
