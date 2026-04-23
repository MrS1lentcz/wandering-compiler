BEGIN;

DROP INDEX IF EXISTS posts_tags_idx;
CREATE INDEX posts_tags_idx ON posts USING gin (tags gin_trgm_ops);

COMMIT;
