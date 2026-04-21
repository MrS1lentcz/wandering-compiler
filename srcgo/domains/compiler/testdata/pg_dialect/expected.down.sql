BEGIN;

DROP INDEX IF EXISTS inventories_payload_gin;
DROP INDEX IF EXISTS inventories_search_idx_gin;
DROP TABLE IF EXISTS inventories;

COMMIT;
