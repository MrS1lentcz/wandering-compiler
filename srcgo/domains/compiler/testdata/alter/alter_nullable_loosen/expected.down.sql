BEGIN;

ALTER TABLE users ADD CONSTRAINT users_email_blank CHECK (email <> '');

ALTER TABLE users ALTER COLUMN email SET NOT NULL;

COMMIT;
