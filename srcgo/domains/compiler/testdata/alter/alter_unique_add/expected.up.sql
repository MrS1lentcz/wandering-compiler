BEGIN;

CREATE UNIQUE INDEX users_email_uidx ON users (email);

COMMIT;
