CREATE TABLE telemetry_send_limits (
    platform TEXT PRIMARY KEY CHECK (platform IN ('android', 'web')),
    last_attempt INTEGER NOT NULL
);
