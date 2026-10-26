ALTER TABLE note_writes ADD COLUMN source_created INTEGER NOT NULL DEFAULT 0;

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
