BEGIN;

DROP INDEX IF EXISTS events_external_id_hash;
DROP INDEX IF EXISTS events_occurred_brin;
DROP INDEX IF EXISTS events_subject_trgm_gin;
DROP INDEX IF EXISTS events_subject_prefix_idx;
DROP INDEX IF EXISTS events_timeline_idx;
DROP TABLE IF EXISTS events;

COMMIT;
