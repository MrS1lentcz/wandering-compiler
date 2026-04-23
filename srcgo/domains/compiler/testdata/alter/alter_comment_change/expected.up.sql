BEGIN;

COMMENT ON COLUMN users.email IS 'login email — unique per tenant';

COMMIT;
