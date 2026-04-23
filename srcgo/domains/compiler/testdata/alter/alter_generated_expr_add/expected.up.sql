BEGIN;

ALTER TABLE users DROP COLUMN full_name;
ALTER TABLE users ADD COLUMN full_name VARCHAR(200) GENERATED ALWAYS AS (first_name || ' ' || last_name) STORED;

COMMIT;
