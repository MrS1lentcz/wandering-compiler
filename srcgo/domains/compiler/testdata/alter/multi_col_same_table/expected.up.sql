BEGIN;

ALTER TABLE users DROP COLUMN phone;

ALTER TABLE users ADD COLUMN website VARCHAR(2048) NULL;
ALTER TABLE users ADD CONSTRAINT users_website_format CHECK (website ~ '^https?://.+$');

ALTER TABLE users RENAME COLUMN name TO display_name;

ALTER TABLE users ALTER COLUMN display_name TYPE VARCHAR(200);

ALTER TABLE users ALTER COLUMN email TYPE VARCHAR(320);

COMMENT ON TABLE users IS 'Multi-axis change on one table: drop phone (#4), add website (#5),
rename name → display_name (#2 stable), widen email (#3 max_len).';

COMMIT;
