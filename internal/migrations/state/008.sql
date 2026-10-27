CREATE TABLE feishu_ai_generations (
    message_id TEXT PRIMARY KEY REFERENCES channel_events(message_id),
    operation TEXT NOT NULL CHECK (operation IN ('insight', 'answer', 'timeline')),
    request BLOB NOT NULL,
    result BLOB,
    status TEXT NOT NULL CHECK (status IN ('running', 'completed', 'failed', 'interrupted')),
    error_code TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE feishu_ai_parts (
    notification_id TEXT PRIMARY KEY REFERENCES notifications(id) ON DELETE CASCADE,
    message_id TEXT NOT NULL REFERENCES feishu_ai_generations(message_id),
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    payload BLOB NOT NULL,
    UNIQUE (message_id, ordinal)
);

-- Each part uses the existing notification attempts and outcome resolution.
-- Aggregate event state only after that notification's own state is durable.
CREATE TRIGGER feishu_ai_notification_status
AFTER UPDATE OF status ON notifications
WHEN EXISTS (SELECT 1 FROM feishu_ai_parts WHERE notification_id = NEW.id)
BEGIN
    UPDATE notifications SET available_at = 1.0e30
    WHERE id = NEW.id AND status = 'pending' AND EXISTS (
        SELECT 1 FROM feishu_ai_parts current
        JOIN feishu_ai_parts previous ON previous.message_id = current.message_id
            AND previous.ordinal < current.ordinal
        JOIN notifications n ON n.id = previous.notification_id
        WHERE current.notification_id = NEW.id AND n.status != 'sent'
    );
    UPDATE notifications SET available_at = unixepoch()
    WHERE NEW.status = 'sent' AND status = 'pending' AND id IN (
        SELECT next.notification_id FROM feishu_ai_parts next
        WHERE next.message_id = NEW.source_event AND NOT EXISTS (
            SELECT 1 FROM feishu_ai_parts previous
            JOIN notifications n ON n.id = previous.notification_id
            WHERE previous.message_id = next.message_id
                AND previous.ordinal < next.ordinal AND n.status != 'sent'
        )
    );
    UPDATE channel_events SET
        status = CASE WHEN EXISTS (
            SELECT 1 FROM notifications n JOIN feishu_ai_parts p ON p.notification_id = n.id
            WHERE p.message_id = NEW.source_event AND n.status != 'sent'
        ) THEN 'processed' ELSE 'succeeded' END,
        reply_status = CASE
            WHEN EXISTS (
                SELECT 1 FROM notifications n JOIN feishu_ai_parts p ON p.notification_id = n.id
                WHERE p.message_id = NEW.source_event AND n.status = 'unknown'
            ) THEN 'unknown'
            WHEN EXISTS (
                SELECT 1 FROM notifications n JOIN feishu_ai_parts p ON p.notification_id = n.id
                WHERE p.message_id = NEW.source_event AND n.status != 'sent'
            ) THEN 'queued' ELSE 'sent' END,
        receipt = CASE WHEN NOT EXISTS (
            SELECT 1 FROM notifications n JOIN feishu_ai_parts p ON p.notification_id = n.id
            WHERE p.message_id = NEW.source_event AND n.status != 'sent'
        ) THEN (
            SELECT n.receipt FROM notifications n JOIN feishu_ai_parts p ON p.notification_id = n.id
            WHERE p.message_id = NEW.source_event ORDER BY p.ordinal DESC LIMIT 1
        ) ELSE NULL END,
        last_error = (
            SELECT n.last_error FROM notifications n JOIN feishu_ai_parts p ON p.notification_id = n.id
            WHERE p.message_id = NEW.source_event AND n.status = 'unknown'
            ORDER BY p.ordinal LIMIT 1
        )
    WHERE message_id = NEW.source_event;
END;
