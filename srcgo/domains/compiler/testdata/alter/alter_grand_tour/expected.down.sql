BEGIN;

COMMENT ON TABLE orders IS NULL;

COMMENT ON TABLE customers IS NULL;

ALTER TABLE orders DROP CONSTRAINT IF EXISTS orders_customer_id_fkey;
ALTER TABLE orders ADD CONSTRAINT orders_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES customers(id) ON DELETE CASCADE;

DROP INDEX IF EXISTS orders_status_idx;

DROP INDEX IF EXISTS customers_email_uidx;

ALTER TABLE orders ALTER COLUMN amount TYPE NUMERIC(10, 2);

ALTER TABLE customers ALTER COLUMN email TYPE VARCHAR(200);

ALTER TABLE customers ALTER COLUMN name TYPE VARCHAR(100);

ALTER TABLE orders RENAME COLUMN amount TO total;

ALTER TABLE orders DROP COLUMN status;
DROP TYPE IF EXISTS orders_status;

ALTER TABLE customers DROP COLUMN address;

COMMIT;
