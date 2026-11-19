ALTER TABLE chat_sessions ADD COLUMN material_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE chat_turns ADD COLUMN material_version INTEGER NOT NULL DEFAULT 1;

WITH changes AS (
    SELECT id, chat_id, position, CASE WHEN json(materials_json) IS
        lag(json(materials_json)) OVER (PARTITION BY chat_id ORDER BY position)
        THEN 0 ELSE 1 END AS changed
    FROM chat_turns
), versions AS (
    SELECT id, sum(changed) OVER (PARTITION BY chat_id ORDER BY position) AS version
    FROM changes
)
UPDATE chat_turns SET material_version = versions.version FROM versions
WHERE chat_turns.id = versions.id;

WITH latest AS (
    SELECT chat_id, material_version, json(materials_json) AS refs,
        row_number() OVER (PARTITION BY chat_id ORDER BY position DESC) AS rank
    FROM chat_turns
)
UPDATE chat_sessions SET material_version = latest.material_version +
    (latest.refs != (SELECT json_group_array(json_object('name', memo_name, 'source_hash', source_hash) ORDER BY position)
                    FROM chat_materials WHERE chat_id = latest.chat_id))
FROM latest WHERE chat_sessions.id = latest.chat_id AND latest.rank = 1;

CREATE TABLE chat_selection_saves (
    chat_id TEXT NOT NULL REFERENCES chat_sessions(id) ON DELETE CASCADE,
    operation_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    write_key TEXT NOT NULL,
    memo_name TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (chat_id, operation_key)
);
