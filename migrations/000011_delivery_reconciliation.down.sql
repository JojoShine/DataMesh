DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM etl_delivery_inputs) OR EXISTS(SELECT 1 FROM etl_reconciliations) THEN
  RAISE EXCEPTION 'Delivery evidence exists; retain evidence before rollback';
 END IF;
END $$;
DROP TABLE delivery_receipts;
DROP TABLE etl_reconciliations;
DROP TABLE etl_delivery_keys;
DROP TABLE etl_delivery_inputs;
