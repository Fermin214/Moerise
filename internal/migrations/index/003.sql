CREATE TABLE star_snapshots (
    version TEXT PRIMARY KEY,
    instance_id TEXT NOT NULL,
    user_name TEXT NOT NULL,
    space_hash TEXT NOT NULL,
    algorithm TEXT NOT NULL,
    frame TEXT NOT NULL,
    generation_id TEXT NOT NULL,
    projection_revision INTEGER NOT NULL,
    generated_at TEXT NOT NULL,
    points_json TEXT NOT NULL,
    topics_json TEXT NOT NULL,
    links_json TEXT NOT NULL
);
CREATE INDEX star_snapshots_owner ON star_snapshots(instance_id, user_name, space_hash, generated_at DESC);
CREATE TABLE star_heads (
    instance_id TEXT NOT NULL,
    user_name TEXT NOT NULL,
    space_hash TEXT NOT NULL,
    version TEXT REFERENCES star_snapshots(version),
    rebuild_token TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(instance_id, user_name)
);
