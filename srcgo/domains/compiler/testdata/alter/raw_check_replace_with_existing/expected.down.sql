BEGIN;

COMMENT ON TABLE products IS NULL;

ALTER TABLE products DROP CONSTRAINT IF EXISTS products_price_stock_sanity;

ALTER TABLE products DROP CONSTRAINT IF EXISTS products_price_positive;
ALTER TABLE products ADD CONSTRAINT products_price_positive CHECK (price > 0);

COMMIT;
