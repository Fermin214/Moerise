ALTER TABLE memos ADD COLUMN create_time TEXT NOT NULL DEFAULT '';
ALTER TABLE memos ADD COLUMN content TEXT NOT NULL DEFAULT '';

UPDATE memos SET
    create_time = COALESCE(json_extract(payload_json, '$.create_time'), update_time),
    content = COALESCE(json_extract(payload_json, '$.content'), '');

CREATE INDEX memos_timeline ON memos(instance_id, user_name, state, create_time DESC, name DESC);

CREATE TABLE memo_tags (
    instance_id TEXT NOT NULL,
    user_name TEXT NOT NULL,
    memo_name TEXT NOT NULL,
    tag TEXT NOT NULL,
    PRIMARY KEY (instance_id, user_name, memo_name, tag),
    FOREIGN KEY (instance_id, user_name, memo_name)
        REFERENCES memos(instance_id, user_name, name) ON DELETE CASCADE
);

CREATE INDEX memo_tags_lookup ON memo_tags(instance_id, user_name, tag, memo_name);

INSERT OR IGNORE INTO memo_tags(instance_id, user_name, memo_name, tag)
SELECT m.instance_id, m.user_name, m.name, CAST(j.value AS TEXT)
FROM memos m, json_each(m.payload_json, '$.tags') j
WHERE json_valid(m.payload_json)
  AND json_type(m.payload_json, '$.tags') = 'array'
  AND j.type = 'text';

CREATE TABLE memo_changes (
    seq INTEGER PRIMARY KEY AUTOINCREMENT,
    instance_id TEXT NOT NULL,
    user_name TEXT NOT NULL,
    name TEXT NOT NULL,
    deleted INTEGER NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX memo_changes_owner_seq ON memo_changes(instance_id, user_name, seq);
CREATE INDEX memo_changes_owner_name ON memo_changes(instance_id, user_name, name);

INSERT INTO memo_changes(instance_id, user_name, name, deleted, updated_at)
SELECT instance_id, user_name, name, 0, observed_at
FROM memos
ORDER BY create_time DESC, name DESC;

ALTER TABLE projection_meta ADD COLUMN sync_generation TEXT NOT NULL DEFAULT '';
ALTER TABLE projection_meta ADD COLUMN min_change_seq INTEGER NOT NULL DEFAULT 0;

UPDATE projection_meta SET sync_generation = lower(hex(randomblob(16))) WHERE sync_generation = '';
