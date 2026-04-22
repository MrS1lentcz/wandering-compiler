BEGIN;

DROP INDEX IF EXISTS catalog_products_sku_uidx;
DROP INDEX IF EXISTS catalog_by_category;
DROP TABLE IF EXISTS catalog_products;

DROP INDEX IF EXISTS catalog_categories_name_uidx;
DROP TABLE IF EXISTS catalog_categories;

COMMIT;
