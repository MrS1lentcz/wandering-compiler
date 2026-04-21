BEGIN;

DROP INDEX IF EXISTS users_username_uidx;
DROP INDEX IF EXISTS users_email_uidx;
DROP INDEX IF EXISTS users_tenant_handle_uidx;
DROP TABLE IF EXISTS users;

COMMIT;
