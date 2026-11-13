DELETE FROM asset_notices WHERE category='product_contract_violated';
ALTER TABLE asset_notices DROP CONSTRAINT asset_notices_category_check;
ALTER TABLE asset_notices ADD CONSTRAINT asset_notices_category_check CHECK(category IN ('sharing_changed','sharing_unavailable','sharing_restored','grant_expiring','grant_expired'));
ALTER TABLE asset_notices DROP COLUMN product_id;
DROP INDEX audit_records_personal_usage;
DROP TABLE asset_product_checks;
DROP TABLE asset_product_revisions;
DROP TABLE asset_products;
