CREATE TABLE review_plans (
    batch_id TEXT PRIMARY KEY REFERENCES review_batches(id),
    instance_id TEXT NOT NULL REFERENCES bindings(instance_id),
    user_name TEXT NOT NULL,
    local_date TEXT NOT NULL,
    settings_version INTEGER NOT NULL,
    rules_json TEXT NOT NULL,
    UNIQUE (instance_id, user_name, local_date)
);

CREATE TABLE review_plan_slots (
    id TEXT PRIMARY KEY,
    batch_id TEXT NOT NULL REFERENCES review_plans(batch_id),
    ordinal INTEGER NOT NULL,
    clock TEXT NOT NULL,
    first_position INTEGER NOT NULL,
    item_count INTEGER NOT NULL,
    UNIQUE (batch_id, clock),
    UNIQUE (batch_id, ordinal)
);

ALTER TABLE notifications ADD COLUMN slot_id TEXT REFERENCES review_plan_slots(id);
