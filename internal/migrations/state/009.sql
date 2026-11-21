ALTER TABLE review_batches ADD COLUMN settings_version INTEGER NOT NULL DEFAULT 0;
ALTER TABLE review_batches ADD COLUMN rules_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE review_batches ADD COLUMN slots_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE review_batches ADD COLUMN scheduled_ordinal INTEGER NOT NULL DEFAULT -1;

UPDATE review_batches SET
    settings_version = COALESCE((SELECT settings_version FROM review_plans WHERE batch_id = review_batches.id), 0),
    rules_json = COALESCE((SELECT rules_json FROM review_plans WHERE batch_id = review_batches.id), '{}'),
    slots_json = COALESCE((SELECT json_group_array(json_object(
        'id', id, 'clock', clock, 'first', first_position, 'count', item_count) ORDER BY ordinal)
        FROM review_plan_slots WHERE batch_id = review_batches.id), '[]'),
    scheduled_ordinal = MAX(
        COALESCE((SELECT MAX(p.ordinal) FROM review_plan_slots p JOIN review_schedule_slots s
            ON s.clock = p.clock AND s.instance_id = review_batches.instance_id
            AND s.user_name = review_batches.user_name AND s.timezone = review_batches.timezone
            AND s.local_date = review_batches.local_date WHERE p.batch_id = review_batches.id), -1),
        COALESCE((SELECT MAX(p.ordinal) FROM review_plan_slots p JOIN notifications n
            ON n.slot_id = p.id WHERE p.batch_id = review_batches.id), -1));

CREATE TABLE delivery_logs (
    id TEXT PRIMARY KEY,
    instance_id TEXT NOT NULL REFERENCES bindings(instance_id),
    user_name TEXT NOT NULL,
    channel_key TEXT NOT NULL,
    channel_kind TEXT NOT NULL,
    logical_key TEXT NOT NULL,
    kind TEXT NOT NULL,
    local_date TEXT NOT NULL,
    batch_id TEXT REFERENCES review_batches(id),
    slot_id TEXT,
    clock TEXT NOT NULL DEFAULT '',
    source_event TEXT,
    status TEXT NOT NULL DEFAULT 'pending',
    platform_uuid TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    available_at REAL NOT NULL,
    receipt TEXT,
    last_error TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    root_id TEXT NOT NULL,
    part_ordinal INTEGER NOT NULL DEFAULT 0,
    part_kind TEXT NOT NULL DEFAULT '',
    memo_name TEXT NOT NULL DEFAULT '',
    source_version TEXT NOT NULL DEFAULT '',
    byte_start INTEGER NOT NULL DEFAULT 0,
    byte_end INTEGER NOT NULL DEFAULT 0,
    attachment_name TEXT NOT NULL DEFAULT '',
    image_key TEXT,
    UNIQUE (instance_id, user_name, channel_key, logical_key),
    UNIQUE (root_id, part_ordinal)
);

INSERT INTO delivery_logs (
    id, instance_id, user_name, channel_key, channel_kind, logical_key, kind, local_date,
    batch_id, slot_id, clock, source_event, status, platform_uuid, attempts, available_at,
    receipt, last_error, created_at, updated_at, root_id, part_ordinal, part_kind,
    memo_name, source_version, byte_start, byte_end, attachment_name, image_key
)
SELECT n.id, n.instance_id, n.user_name, n.channel_key, n.channel_kind, n.logical_key, n.kind, n.local_date,
    n.batch_id, n.slot_id, COALESCE(s.clock, ''), n.source_event,
    CASE WHEN n.status IN ('running', 'unknown') THEN 'failed'
        WHEN n.status = 'skipped' AND n.last_error = 'PART_INTERRUPTED' THEN 'pending'
        WHEN n.status = 'skipped' THEN 'failed' ELSE n.status END,
    n.platform_uuid, n.attempts,
    CASE WHEN n.available_at > 1.0e20 THEN 0 ELSE n.available_at END,
    n.receipt,
    CASE WHEN n.status = 'running' THEN 'PROCESS_INTERRUPTED'
        WHEN n.status = 'unknown' THEN COALESCE(n.last_error, 'SEND_OUTCOME_UNKNOWN') ELSE n.last_error END,
    n.created_at, n.updated_at, COALESCE(p.root_id, n.id), COALESCE(p.position, 0),
    COALESCE(p.kind, ''), COALESCE(p.memo_name, ''), COALESCE(p.source_version, ''),
    COALESCE(p.byte_start, 0), COALESCE(p.byte_end, 0), COALESCE(p.attachment_name, ''), p.image_key
FROM notifications n
LEFT JOIN review_delivery_parts p ON p.notification_id = n.id
LEFT JOIN review_plan_slots s ON s.id = n.slot_id;

CREATE INDEX delivery_ready ON delivery_logs(status, available_at);
CREATE INDEX delivery_owner_history ON delivery_logs(instance_id, user_name, created_at DESC, id DESC);
CREATE INDEX delivery_inbox ON delivery_logs(source_event) WHERE source_event IS NOT NULL;

DROP TRIGGER review_delivery_hold;
DROP TRIGGER review_delivery_next;
DROP TABLE notification_attempts;
DROP TABLE notification_resolutions;
DROP TABLE review_delivery_parts;
DROP TABLE notifications;
DROP TABLE review_schedule_slots;
DROP TABLE notification_rate_limits;
DROP TABLE review_plan_slots;
DROP TABLE review_plans;

ALTER TABLE bot_inbox ADD COLUMN result_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE bot_inbox ADD COLUMN resource_name TEXT NOT NULL DEFAULT '';

INSERT OR IGNORE INTO bot_bindings (instance_id,user_name,kind,bot_id,peer,bound_at)
SELECT b.instance_id,b.user_name,'feishu-app',e.app_id,e.sender_open_id,
    CAST(unixepoch(e.created_at,'subsec')*1000 AS INTEGER)
FROM channel_events e CROSS JOIN bindings b ORDER BY e.created_at;

INSERT OR IGNORE INTO bot_inbox (write_key,instance_id,user_name,kind,bot_id,peer,message_id,
    msg_index,digest,operation,body,received_at,message_at,phase,reply,batch_id,receipt,error_code,
    available_at,result_kind,resource_name)
SELECT 'feishu:'||e.message_id,b.instance_id,b.user_name,'feishu-app',e.app_id,e.sender_open_id,
    e.message_id,e.event_id,e.event_hash,'feishu-legacy',e.payload,
    CAST(unixepoch(e.created_at,'subsec')*1000 AS INTEGER),CAST(unixepoch(e.created_at,'subsec')*1000 AS INTEGER),
    CASE WHEN EXISTS(SELECT 1 FROM delivery_logs d WHERE d.source_event=e.message_id) THEN 'logged'
        WHEN e.status='succeeded' THEN 'sent'
        WHEN e.status IN ('pending','processing') THEN 'pending'
        WHEN e.reply_status='unknown' THEN 'failed' ELSE 'reply' END,
    '',CASE WHEN e.result_kind='review' THEN e.resource_name END,e.receipt,e.last_error,
    CAST(e.available_at*1000 AS INTEGER),COALESCE(e.result_kind,''),COALESCE(e.resource_name,'')
FROM channel_events e CROSS JOIN bindings b;
DROP TABLE channel_events;
