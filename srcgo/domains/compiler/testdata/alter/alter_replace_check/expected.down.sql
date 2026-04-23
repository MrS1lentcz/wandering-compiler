BEGIN;

ALTER TABLE users DROP CONSTRAINT IF EXISTS users_name_len;
ALTER TABLE users ADD CONSTRAINT users_name_len CHECK (char_length(name) >= 3);

COMMIT;
