CREATE TABLE legacy_stargazing_deployments (
    instance_id TEXT PRIMARY KEY REFERENCES bindings(instance_id)
);

INSERT INTO legacy_stargazing_deployments
SELECT instance_id FROM bindings
WHERE instance_id NOT IN (SELECT instance_id FROM product_settings);
