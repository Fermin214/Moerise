CREATE TABLE feishu_review_parts (
    notification_id TEXT PRIMARY KEY REFERENCES notifications(id) ON DELETE CASCADE,
    root_id TEXT NOT NULL REFERENCES notifications(id),
    position INTEGER NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('text', 'image', 'attachment_link', 'source_changed', 'empty')),
    memo_name TEXT NOT NULL,
    source_version TEXT NOT NULL,
    attachment_name TEXT NOT NULL DEFAULT '',
    byte_start INTEGER NOT NULL DEFAULT 0,
    byte_end INTEGER NOT NULL DEFAULT 0,
    image_key TEXT,
    UNIQUE (root_id, position)
);

-- The queue owns attempts, UUIDs and receipts. Only a confirmed predecessor
-- releases its successor, including confirmations made through Resolve.
CREATE TRIGGER feishu_review_next
AFTER UPDATE OF status ON notifications
WHEN NEW.status = 'sent' AND OLD.status <> 'sent'
BEGIN
    UPDATE notifications SET available_at = 0
    WHERE status = 'pending' AND id IN (
        SELECT next.notification_id FROM feishu_review_parts previous
        JOIN feishu_review_parts next
            ON next.root_id = previous.root_id AND next.position = previous.position + 1
        WHERE previous.notification_id = NEW.id
    );
END;
