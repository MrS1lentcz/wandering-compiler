BEGIN;

ALTER TABLE products DROP CONSTRAINT IF EXISTS products_price_positive;
ALTER TABLE products ADD CONSTRAINT products_price_positive CHECK (price >= 0.01);

COMMIT;
