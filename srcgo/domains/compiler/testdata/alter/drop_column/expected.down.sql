BEGIN;

ALTER TABLE users ADD COLUMN legacy_token TEXT NOT NULL;
ALTER TABLE users ADD CONSTRAINT users_legacy_token_blank CHECK (legacy_token <> '');

COMMIT;
