CREATE TABLE map_layouts (
    instance_id TEXT NOT NULL,
    user_name TEXT NOT NULL,
    space_hash TEXT NOT NULL,
    algorithm TEXT NOT NULL,
    generation_id TEXT NOT NULL,
    projection_revision INTEGER NOT NULL,
    version TEXT NOT NULL,
    basis_json TEXT NOT NULL,
    PRIMARY KEY (instance_id, user_name)
);

CREATE TABLE map_points (
    instance_id TEXT NOT NULL,
    user_name TEXT NOT NULL,
    name TEXT NOT NULL,
    source_hash TEXT NOT NULL,
    vector_hash TEXT NOT NULL,
    x REAL NOT NULL,
    y REAL NOT NULL,
    semantic INTEGER NOT NULL CHECK (semantic IN (0, 1)),
    PRIMARY KEY (instance_id, user_name, name),
    FOREIGN KEY (instance_id, user_name)
        REFERENCES map_layouts(instance_id, user_name) ON DELETE CASCADE
);
