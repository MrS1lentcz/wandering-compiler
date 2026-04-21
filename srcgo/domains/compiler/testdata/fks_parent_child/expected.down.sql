BEGIN;

DROP INDEX IF EXISTS orders_assigned_to_idx;
DROP INDEX IF EXISTS orders_customer_id_status_idx;
DROP TABLE IF EXISTS orders;

DROP TABLE IF EXISTS customers;

DROP TABLE IF EXISTS categories;

COMMIT;
