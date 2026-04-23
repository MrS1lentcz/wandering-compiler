BEGIN;

ALTER TABLE users ADD COLUMN email VARCHAR(255) NOT NULL;
ALTER TABLE users ADD CONSTRAINT users_email_blank CHECK (email <> '');
ALTER TABLE users ADD CONSTRAINT users_email_format CHECK (email ~ '^[^@\s]+@[^@\s]+\.[^@\s]+$');

CREATE UNIQUE INDEX users_email_uidx ON users (email);

COMMIT;
