BEGIN;

CREATE INDEX posts_content_lower_idx ON posts (lower(content));

COMMIT;
