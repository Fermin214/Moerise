CREATE TABLE review_schedule_slots (
    instance_id TEXT NOT NULL REFERENCES bindings(instance_id),
    user_name TEXT NOT NULL,
    channel_key TEXT NOT NULL,
    timezone TEXT NOT NULL,
    local_date TEXT NOT NULL,
    clock TEXT NOT NULL,
    PRIMARY KEY (instance_id, user_name, channel_key, timezone, local_date, clock)
);

INSERT INTO review_schedule_slots
    (instance_id, user_name, channel_key, timezone, local_date, clock)
SELECT instance_id, user_name, channel_key, timezone, last_date, ''
FROM notification_schedules;
