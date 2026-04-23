BEGIN;

DROP INDEX IF EXISTS users_email_idx;
CREATE INDEX users_email_idx ON users (email) INCLUDE (name, phone);

COMMENT ON TABLE users IS 'INCLUDE columns changed — ReplaceIndex with new include list.';

COMMIT;
