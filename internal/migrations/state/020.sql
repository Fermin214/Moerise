ALTER TABLE feishu_ai_generations ADD COLUMN content_cleaned_at TEXT;
CREATE INDEX notifications_source_event ON notifications(source_event) WHERE source_event IS NOT NULL;

-- Keep part identities, ordering, receipts and resolutions after dropping content.
CREATE TRIGGER feishu_ai_content_cleanup
AFTER UPDATE OF status, reply_status ON channel_events
WHEN NEW.result_kind = 'ai' AND NEW.status = 'succeeded' AND NEW.reply_status = 'sent'
BEGIN
    UPDATE feishu_ai_generations
    SET request = X'', result = NULL, content_cleaned_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
    WHERE message_id = NEW.message_id AND status <> 'running' AND content_cleaned_at IS NULL
        AND EXISTS (SELECT 1 FROM feishu_ai_parts WHERE message_id = NEW.message_id)
        AND (SELECT COUNT(*) FROM feishu_ai_parts WHERE message_id = NEW.message_id)
            = (SELECT MAX(ordinal) + 1 FROM feishu_ai_parts WHERE message_id = NEW.message_id)
        AND NOT EXISTS (
            SELECT 1 FROM feishu_ai_parts p LEFT JOIN notifications n ON n.id = p.notification_id
            WHERE p.message_id = NEW.message_id
                AND (n.id IS NULL OR n.status <> 'sent' OR n.kind <> 'reply'
                    OR n.source_event IS NOT p.message_id)
        )
        AND NOT EXISTS (
            SELECT 1 FROM notifications n
            WHERE n.source_event = NEW.message_id AND NOT EXISTS (
                SELECT 1 FROM feishu_ai_parts p
                WHERE p.notification_id = n.id AND p.message_id = NEW.message_id
            )
        );
    UPDATE feishu_ai_parts SET payload = X''
    WHERE message_id = NEW.message_id AND EXISTS (
        SELECT 1 FROM feishu_ai_generations g
        WHERE g.message_id = NEW.message_id AND g.content_cleaned_at IS NOT NULL
    );
END;

UPDATE channel_events SET status = status
WHERE result_kind = 'ai' AND status = 'succeeded' AND reply_status = 'sent';
