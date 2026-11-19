CREATE TABLE legacy_channel_deployments (
    instance_id TEXT PRIMARY KEY REFERENCES bindings(instance_id)
);

INSERT INTO legacy_channel_deployments
SELECT instance_id FROM bindings
WHERE instance_id NOT IN (SELECT instance_id FROM product_settings);
