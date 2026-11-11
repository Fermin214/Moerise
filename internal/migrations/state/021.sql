CREATE TABLE notification_rate_limits (
    provider_key TEXT PRIMARY KEY,
    retry_at REAL NOT NULL
);
