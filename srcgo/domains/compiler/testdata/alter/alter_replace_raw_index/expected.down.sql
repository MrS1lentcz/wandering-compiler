BEGIN;

DROP INDEX IF EXISTS posts_title_idx;
CREATE INDEX posts_title_idx ON posts (title);

COMMIT;
