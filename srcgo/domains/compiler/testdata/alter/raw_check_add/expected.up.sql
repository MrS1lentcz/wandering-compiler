BEGIN;

ALTER TABLE orders ADD CONSTRAINT orders_dates_ordered CHECK (start_date <= end_date);

COMMIT;
