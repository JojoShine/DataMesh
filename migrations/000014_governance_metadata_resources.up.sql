ALTER TABLE metadata_observations ADD COLUMN resource_type text NOT NULL DEFAULT 'raw_object' CHECK(resource_type IN ('raw_object','governed_dataset','table_resource','file_resource','api_resource'));
ALTER TABLE metadata_observations ADD COLUMN observation_kind text NOT NULL DEFAULT 'live_schema' CHECK(observation_kind IN ('live_schema','registered_contract'));
