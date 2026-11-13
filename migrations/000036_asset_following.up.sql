CREATE TABLE asset_saved_resources (
 id text PRIMARY KEY,
 user_id text NOT NULL REFERENCES users(id),
 resource_type text NOT NULL CHECK(resource_type IN ('raw_object','governed_dataset','table_resource','file_resource','api_resource')),
 resource_id text NOT NULL,
 contract_version integer NOT NULL CHECK(contract_version>0),
 resource_name text NOT NULL,
 directory_id text NOT NULL,
 bookmarked boolean NOT NULL DEFAULT false,
 following boolean NOT NULL DEFAULT false,
 fingerprint text NOT NULL,
 available boolean NOT NULL,
 observation integer NOT NULL DEFAULT 0,
 version integer NOT NULL DEFAULT 1,
 checked_at timestamptz NOT NULL DEFAULT now(),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(user_id,resource_type,resource_id,contract_version)
);
CREATE INDEX asset_saved_personal ON asset_saved_resources(user_id,updated_at DESC,id);
CREATE TABLE asset_notices (
 id text PRIMARY KEY,
 user_id text NOT NULL REFERENCES users(id),
 dedupe_key text NOT NULL,
 category text NOT NULL CHECK(category IN ('sharing_changed','sharing_unavailable','sharing_restored','grant_expiring','grant_expired')),
 resource_name text NOT NULL,
 resource_id text NOT NULL,
 contract_version integer NOT NULL,
 message text NOT NULL,
 read_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(user_id,dedupe_key)
);
CREATE INDEX asset_notices_personal ON asset_notices(user_id,created_at DESC,id);
GRANT SELECT,INSERT,UPDATE ON asset_saved_resources,asset_notices TO datamesh_app;
