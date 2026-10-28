-- A legacy root may be durable before notification_schedules.last_date advances.
-- An empty clock consumes only the first configured slot for that date.
INSERT OR IGNORE INTO review_schedule_slots
    (instance_id, user_name, channel_key, timezone, local_date, clock)
SELECT n.instance_id, n.user_name, n.channel_key, b.timezone, n.local_date, ''
FROM notifications n
JOIN review_batches b ON b.id = n.batch_id
    AND b.instance_id = n.instance_id AND b.user_name = n.user_name
    AND b.local_date = n.local_date
WHERE n.kind = 'review' AND n.source_event IS NULL
    AND n.logical_key = n.local_date || ':' || n.batch_id
    AND NOT EXISTS (
        SELECT 1 FROM feishu_review_parts p
        WHERE p.notification_id = n.id AND p.root_id <> n.id
    );
