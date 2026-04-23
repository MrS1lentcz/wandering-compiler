BEGIN;

COMMENT ON TABLE reporting.events IS NULL;

ALTER TABLE reporting.events DROP COLUMN severity;
DROP TYPE IF EXISTS reporting.events_severity;

COMMIT;
