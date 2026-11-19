CREATE TEMP TABLE saved_attempts AS SELECT * FROM notification_attempts;
CREATE TEMP TABLE saved_resolutions AS SELECT * FROM notification_resolutions;
CREATE TEMP TABLE saved_parts AS SELECT * FROM review_delivery_parts;
DROP TRIGGER review_delivery_next;
DROP TABLE notification_attempts;
DROP TABLE notification_resolutions;
DROP TABLE review_delivery_parts;

CREATE TABLE notification_queue (
    id TEXT PRIMARY KEY,
    instance_id TEXT NOT NULL REFERENCES bindings(instance_id),
    user_name TEXT NOT NULL,
    channel_key TEXT NOT NULL,
    logical_key TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('review', 'reply')),
    local_date TEXT NOT NULL,
    batch_id TEXT REFERENCES review_batches(id),
    source_event TEXT REFERENCES channel_events(message_id),
    status TEXT NOT NULL CHECK (status IN ('pending', 'running', 'unknown', 'sent', 'failed', 'skipped')),
    platform_uuid TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    available_at REAL NOT NULL,
    receipt TEXT,
    last_error TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    slot_id TEXT REFERENCES review_plan_slots(id),
    channel_kind TEXT NOT NULL DEFAULT '',
    UNIQUE (instance_id, user_name, channel_key, logical_key)
);
INSERT INTO notification_queue SELECT *, CASE WHEN kind = 'reply' THEN 'feishu-app' ELSE '' END FROM notifications;
DROP TABLE notifications;
ALTER TABLE notification_queue RENAME TO notifications;
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
INSERT INTO notification_attempts SELECT * FROM saved_attempts;
DROP TABLE saved_attempts;
CREATE TABLE notification_resolutions (
    id TEXT PRIMARY KEY,
    notification_id TEXT NOT NULL REFERENCES notifications(id),
    decision TEXT NOT NULL,
    receipt TEXT,
    resolved_by TEXT NOT NULL,
    created_at TEXT NOT NULL
);
INSERT INTO notification_resolutions SELECT * FROM saved_resolutions;
DROP TABLE saved_resolutions;
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
INSERT INTO review_delivery_parts SELECT * FROM saved_parts;
DROP TABLE saved_parts;

UPDATE notifications SET status = 'skipped', last_error = 'PART_INTERRUPTED'
WHERE status = 'pending' AND id IN (
    SELECT rest.notification_id FROM review_delivery_parts previous
    JOIN notifications uncertain ON uncertain.id = previous.notification_id AND uncertain.status = 'unknown'
    JOIN review_delivery_parts rest ON rest.root_id = previous.root_id AND rest.position > previous.position
);

CREATE TRIGGER review_delivery_hold
AFTER UPDATE OF status ON notifications
WHEN NEW.status IN ('unknown','failed') AND OLD.status = 'running'
BEGIN
    UPDATE notifications SET status = 'skipped', last_error = 'PART_INTERRUPTED'
    WHERE status = 'pending' AND id IN (
        SELECT rest.notification_id FROM review_delivery_parts previous
        JOIN review_delivery_parts rest
            ON rest.root_id = previous.root_id AND rest.position > previous.position
        WHERE previous.notification_id = NEW.id
    );
END;

CREATE TRIGGER review_delivery_next
AFTER UPDATE OF status ON notifications
WHEN NEW.status = 'sent' AND OLD.status <> 'sent'
BEGIN
    UPDATE notifications SET status = 'pending', available_at = 0, last_error = NULL
    WHERE (status = 'pending' OR status = 'skipped' AND last_error = 'PART_INTERRUPTED') AND id IN (
        SELECT next.notification_id FROM review_delivery_parts previous
        JOIN review_delivery_parts next
            ON next.root_id = previous.root_id AND next.position = previous.position + 1
        WHERE previous.notification_id = NEW.id
    );
END;
