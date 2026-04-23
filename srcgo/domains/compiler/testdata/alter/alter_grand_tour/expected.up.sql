BEGIN;

ALTER TABLE customers ADD COLUMN address JSONB NOT NULL;

CREATE TYPE orders_status AS ENUM ('FREE', 'PRO', 'ENTERPRISE');

ALTER TABLE orders ADD COLUMN status orders_status NOT NULL;

ALTER TABLE orders RENAME COLUMN total TO amount;

ALTER TABLE customers ALTER COLUMN name TYPE VARCHAR(120);

ALTER TABLE customers ALTER COLUMN email TYPE VARCHAR(255);

ALTER TABLE orders ALTER COLUMN amount TYPE NUMERIC(19, 4);

CREATE UNIQUE INDEX customers_email_uidx ON customers (email);

CREATE INDEX orders_status_idx ON orders (status);

ALTER TABLE orders DROP CONSTRAINT IF EXISTS orders_customer_id_fkey;
ALTER TABLE orders ADD CONSTRAINT orders_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES customers(id) ON DELETE RESTRICT;

COMMENT ON TABLE customers IS 'tenant customers';

COMMENT ON TABLE orders IS 'Order: precision widened + new status enum + renamed "total" → "amount"';

COMMIT;
