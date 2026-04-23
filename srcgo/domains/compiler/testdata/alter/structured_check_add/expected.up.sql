BEGIN;

ALTER TABLE users ADD CONSTRAINT users_name_len CHECK (char_length(name) >= 3);

COMMIT;
