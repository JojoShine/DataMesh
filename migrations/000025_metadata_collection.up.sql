CREATE TABLE metadata_collection_policies (
 object_id text PRIMARY KEY,
 department_id text NOT NULL,
 actor_id text NOT NULL REFERENCES users(id),
 requested boolean NOT NULL DEFAULT false,
 enabled boolean NOT NULL DEFAULT true,
 interval_minutes integer NOT NULL DEFAULT 1440 CHECK(interval_minutes BETWEEN 15 AND 43200),
 version integer NOT NULL DEFAULT 1 CHECK(version>0),
 next_at timestamptz NOT NULL DEFAULT now(),
 last_attempt_at timestamptz,
 last_success_at timestamptz,
 failures integer NOT NULL DEFAULT 0 CHECK(failures>=0),
 error_code text NOT NULL DEFAULT '',
 source_version integer NOT NULL DEFAULT 0,
 object_version integer NOT NULL DEFAULT 0,
 observation_id text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX metadata_collection_due ON metadata_collection_policies(next_at) WHERE enabled;
CREATE TABLE metadata_collection_runs (
 id text PRIMARY KEY,
 object_id text NOT NULL REFERENCES metadata_collection_policies(object_id),
 actor_id text NOT NULL REFERENCES users(id),
 trigger text NOT NULL CHECK(trigger IN ('initial','scheduled','resource_changed','manual')),
 state text NOT NULL CHECK(state IN ('succeeded','failed','blocked')),
 observation_id text NOT NULL DEFAULT '',
 changed boolean NOT NULL DEFAULT false,
 error_code text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX metadata_collection_history ON metadata_collection_runs(object_id,created_at DESC,id);
CREATE TABLE metadata_change_reviews (
 observation_id text PRIMARY KEY REFERENCES metadata_observations(id),
 object_id text NOT NULL,
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','acknowledged','deferred')),
 note text NOT NULL DEFAULT '',
 reviewed_by text REFERENCES users(id),
 reviewed_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX metadata_change_pending ON metadata_change_reviews(object_id,created_at DESC) WHERE state='pending';
INSERT INTO metadata_change_reviews(observation_id,object_id,created_at)
SELECT id,object_id,created_at FROM (
 SELECT DISTINCT ON (object_id) id,object_id,created_at,matches_active,changes
 FROM metadata_observations ORDER BY object_id,version DESC
) latest WHERE NOT matches_active OR jsonb_array_length(changes)>0;
GRANT SELECT,INSERT,UPDATE ON metadata_collection_policies,metadata_change_reviews TO datamesh_app;
GRANT SELECT,INSERT ON metadata_collection_runs TO datamesh_app;
