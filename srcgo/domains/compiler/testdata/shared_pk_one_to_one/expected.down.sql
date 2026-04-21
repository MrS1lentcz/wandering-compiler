BEGIN;

DROP TABLE IF EXISTS admin_extras;

DROP INDEX IF EXISTS user_profiles_email_uidx;
DROP TABLE IF EXISTS user_profiles;

COMMIT;
