CREATE TABLE discovery_reads (
    instance_id TEXT NOT NULL REFERENCES bindings(instance_id),
    user_name TEXT NOT NULL,
    memo_name TEXT NOT NULL,
    opened_at TEXT NOT NULL,
    PRIMARY KEY (instance_id, user_name, memo_name)
);
