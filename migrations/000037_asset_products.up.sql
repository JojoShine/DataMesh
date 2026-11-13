CREATE TABLE asset_products (
 id text PRIMARY KEY,
 department_id text NOT NULL,
 owner_id text NOT NULL REFERENCES users(id),
 state text NOT NULL CHECK(state IN ('draft','published','deprecated','retired')),
 version integer NOT NULL DEFAULT 1 CHECK(version>0),
 draft_id text NOT NULL DEFAULT '',
 published_id text NOT NULL DEFAULT '',
 replacement_id text NOT NULL DEFAULT '',
 reason text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE asset_product_revisions (
 id text PRIMARY KEY,
 product_id text NOT NULL REFERENCES asset_products(id),
 state text NOT NULL CHECK(state IN ('draft','submitted','published','rejected')),
 definition jsonb NOT NULL,
 members jsonb NOT NULL,
 semantics jsonb NOT NULL,
 reviewer_id text NOT NULL DEFAULT '',
 review_note text NOT NULL DEFAULT '',
 valid_until timestamptz,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE asset_product_checks (
 id text PRIMARY KEY,
 product_id text NOT NULL REFERENCES asset_products(id),
 revision_id text NOT NULL REFERENCES asset_product_revisions(id),
 actor_id text NOT NULL REFERENCES users(id),
 state text NOT NULL CHECK(state IN ('passed','violated','unknown')),
 items jsonb NOT NULL,
 checked_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX asset_products_department ON asset_products(department_id,updated_at DESC,id);
CREATE INDEX asset_products_published ON asset_products(state,updated_at DESC,id) WHERE published_id<>'';
CREATE INDEX asset_product_revisions_product ON asset_product_revisions(product_id,created_at DESC,id);
CREATE INDEX asset_product_checks_personal ON asset_product_checks(actor_id,product_id,checked_at DESC,id);
CREATE INDEX audit_records_personal_usage ON audit_records(actor_id,created_at) WHERE result='success' AND action IN ('deliver_raw_object','deliver_governed_dataset','deliver_table','download_file','invoke_api');
GRANT SELECT,INSERT,UPDATE ON asset_products,asset_product_revisions TO datamesh_app;
GRANT SELECT,INSERT ON asset_product_checks TO datamesh_app;

ALTER TABLE asset_notices ADD COLUMN product_id text NOT NULL DEFAULT '';
ALTER TABLE asset_notices DROP CONSTRAINT asset_notices_category_check;
ALTER TABLE asset_notices ADD CONSTRAINT asset_notices_category_check CHECK(category IN ('sharing_changed','sharing_unavailable','sharing_restored','grant_expiring','grant_expired','product_contract_violated'));
