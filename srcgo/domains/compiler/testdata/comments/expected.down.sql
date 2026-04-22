BEGIN;

DROP INDEX IF EXISTS subscribers_email_uidx;
DROP TABLE IF EXISTS subscribers;

DROP INDEX IF EXISTS articles_byline_idx;
DROP TABLE IF EXISTS articles;

COMMIT;
