BEGIN;

DROP TABLE IF EXISTS ye_olde_profiles;

DROP TABLE IF EXISTS product_category;

DROP INDEX IF EXISTS product_sku_uidx;
DROP TABLE IF EXISTS product;

DROP TABLE IF EXISTS dashboard_url_field;

COMMIT;
