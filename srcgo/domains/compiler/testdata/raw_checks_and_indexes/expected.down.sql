BEGIN;

DROP INDEX IF EXISTS bookings_lower_customer_idx;
DROP INDEX IF EXISTS bookings_email_active_uidx;
DROP TABLE IF EXISTS bookings;

COMMIT;
