BEGIN;

DROP INDEX IF EXISTS users_email_idx;
CREATE UNIQUE INDEX users_email_idx ON users (email);

COMMIT;
