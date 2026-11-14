ALTER TABLE map_layouts ADD COLUMN topics_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE map_layouts ADD COLUMN links_json TEXT NOT NULL DEFAULT '[]';
