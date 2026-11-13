DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM users WHERE public_test) THEN
  RAISE EXCEPTION 'Public test identities retain a mandatory boundary; do not remove it while these identities exist';
 END IF;
END $$;
ALTER TABLE users DROP COLUMN public_test;
