BEGIN;

DROP INDEX IF EXISTS articles_email_uidx;
DROP TABLE IF EXISTS articles;

COMMIT;
