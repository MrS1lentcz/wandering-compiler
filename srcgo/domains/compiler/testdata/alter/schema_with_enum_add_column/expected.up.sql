BEGIN;

CREATE TYPE reporting.events_severity AS ENUM ('INFO', 'WARN', 'CRIT');

ALTER TABLE reporting.events ADD COLUMN severity reporting.events_severity NOT NULL;

COMMENT ON TABLE reporting.events IS 'Exercise SCHEMA-qualified CREATE TYPE + ADD COLUMN — ENUM type
name qualifies with `reporting.events_severity` prefix.';

COMMIT;
