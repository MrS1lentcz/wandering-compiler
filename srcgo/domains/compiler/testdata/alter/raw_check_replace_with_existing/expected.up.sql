BEGIN;

ALTER TABLE products DROP CONSTRAINT IF EXISTS products_price_positive;
ALTER TABLE products ADD CONSTRAINT products_price_positive CHECK (price >= 0.01);

ALTER TABLE products ADD CONSTRAINT products_price_stock_sanity CHECK (price * stock >= 0);

COMMENT ON TABLE products IS 'One raw_check replaced (body change), one unchanged, one added.';

COMMIT;
