BEGIN;

DROP INDEX IF EXISTS subscribers_email_uidx;
DROP TABLE IF EXISTS subscribers;

DROP TABLE IF EXISTS articles;

COMMIT;
