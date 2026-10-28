UPDATE feishu_review_parts SET kind = 'empty'
WHERE kind IN ('image', 'attachment_link');

ALTER TABLE feishu_review_parts DROP COLUMN attachment_name;
ALTER TABLE feishu_review_parts DROP COLUMN image_key;
