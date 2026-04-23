BEGIN;

ALTER TABLE orders DROP CONSTRAINT IF EXISTS orders_customer_id_fkey;

COMMIT;
