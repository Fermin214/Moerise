CREATE TABLE product_settings (
    instance_id TEXT PRIMARY KEY REFERENCES bindings(instance_id),
    version INTEGER NOT NULL CHECK (version > 0),
    ciphertext BLOB NOT NULL,
    updated_at TEXT NOT NULL
);
