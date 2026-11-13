CREATE TABLE governance_workspaces (
 id text PRIMARY KEY,
 department_id text NOT NULL,
 name text NOT NULL CHECK(length(btrim(name)) BETWEEN 1 AND 200),
 description text NOT NULL DEFAULT '' CHECK(length(description)<=4000),
 owner_id text REFERENCES users(id),
 state text NOT NULL DEFAULT 'active' CHECK(state IN ('active','archived')),
 is_legacy boolean NOT NULL DEFAULT false,
 max_concurrent_runs integer NOT NULL DEFAULT 2 CHECK(max_concurrent_runs BETWEEN 1 AND 10),
 version integer NOT NULL DEFAULT 1 CHECK(version>0),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(id,department_id)
);
CREATE UNIQUE INDEX governance_workspace_legacy_department ON governance_workspaces(department_id) WHERE is_legacy;
CREATE INDEX governance_workspace_department ON governance_workspaces(department_id,state,updated_at DESC,id);

-- The legacy default also supports old writers and departments created after migration.
CREATE FUNCTION governance_legacy_workspace(dept text) RETURNS text LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
DECLARE result text := md5('governance-workspace:' || dept);
BEGIN
 INSERT INTO public.governance_workspaces(id,department_id,name,is_legacy)
 VALUES(result,dept,'历史治理',true) ON CONFLICT DO NOTHING;
 RETURN result;
END $$;
SELECT governance_legacy_workspace(department_id) FROM (
 SELECT department_id FROM users UNION SELECT department_id FROM governance_definitions
 UNION SELECT department_id FROM governance_runs UNION SELECT department_id FROM governance_schedules
 UNION SELECT department_id FROM business_records WHERE kind='dataset'
) departments;

ALTER TABLE governance_definitions ADD COLUMN workspace_id text;
UPDATE governance_definitions SET workspace_id=md5('governance-workspace:' || department_id) WHERE kind IN ('model','dataflow','workflow','quality_template');
ALTER TABLE governance_definitions ADD CONSTRAINT governance_definition_workspace_kind CHECK((kind IN ('model','dataflow','workflow','quality_template'))=(workspace_id IS NOT NULL));
ALTER TABLE governance_definitions ADD CONSTRAINT governance_definition_workspace_department FOREIGN KEY(workspace_id,department_id) REFERENCES governance_workspaces(id,department_id);
CREATE INDEX governance_definition_workspace ON governance_definitions(workspace_id,kind,created_at DESC,id);

CREATE FUNCTION governance_definition_workspace_default() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
BEGIN
 IF NEW.kind IN ('model','dataflow','workflow','quality_template') AND (NEW.workspace_id IS NULL OR NEW.workspace_id='') THEN
  NEW.workspace_id := public.governance_legacy_workspace(NEW.department_id);
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER governance_definition_workspace_default BEFORE INSERT ON governance_definitions FOR EACH ROW EXECUTE FUNCTION governance_definition_workspace_default();

ALTER TABLE governance_runs ADD COLUMN workspace_id text;
UPDATE governance_runs SET workspace_id=md5('governance-workspace:' || department_id);
ALTER TABLE governance_runs ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE governance_runs ADD CONSTRAINT governance_run_workspace_department FOREIGN KEY(workspace_id,department_id) REFERENCES governance_workspaces(id,department_id);
CREATE INDEX governance_run_workspace ON governance_runs(workspace_id,state,created_at DESC,id);
ALTER TABLE governance_schedules ADD COLUMN workspace_id text;
UPDATE governance_schedules SET workspace_id=md5('governance-workspace:' || department_id);
ALTER TABLE governance_schedules ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE governance_schedules ADD CONSTRAINT governance_schedule_workspace_department FOREIGN KEY(workspace_id,department_id) REFERENCES governance_workspaces(id,department_id);
CREATE FUNCTION governance_task_workspace_default() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
BEGIN
 IF NEW.workspace_id IS NULL OR NEW.workspace_id='' THEN NEW.workspace_id:=public.governance_legacy_workspace(NEW.department_id); END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER governance_run_workspace_default BEFORE INSERT ON governance_runs FOR EACH ROW EXECUTE FUNCTION governance_task_workspace_default();
CREATE TRIGGER governance_schedule_workspace_default BEFORE INSERT ON governance_schedules FOR EACH ROW EXECUTE FUNCTION governance_task_workspace_default();

CREATE TABLE governance_workspace_sources (
 workspace_id text NOT NULL REFERENCES governance_workspaces(id),
 resource_type text NOT NULL CHECK(resource_type IN ('raw_object','governed_dataset','table_resource','file_resource','api_resource')),
 resource_id text NOT NULL,
 contract_version integer NOT NULL CHECK(contract_version>0),
 actor_id text NOT NULL REFERENCES users(id),
 active boolean NOT NULL DEFAULT true,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(workspace_id,resource_type,resource_id,contract_version)
);
-- Carry declared historical inputs into the source view without rewriting a definition.
INSERT INTO governance_workspace_sources(workspace_id,resource_type,resource_id,contract_version,actor_id)
 SELECT DISTINCT ON(d.workspace_id,n->'source'->'resource'->>'resource_type',n->'source'->'resource'->>'resource_id',(n->'source'->'resource'->>'contract_version')::int)
 d.workspace_id,n->'source'->'resource'->>'resource_type',n->'source'->'resource'->>'resource_id',(n->'source'->'resource'->>'contract_version')::int,v.created_by
 FROM governance_definitions d JOIN governance_versions v ON v.definition_id=d.id
 CROSS JOIN LATERAL jsonb_array_elements(COALESCE(v.definition->'dataflow'->'nodes','[]')) n
 WHERE d.kind='dataflow' AND n->'source'->'resource'->>'resource_type' IN ('raw_object','governed_dataset','table_resource','file_resource','api_resource') AND length(n->'source'->'resource'->>'resource_id')>0 AND n->'source'->'resource'->>'contract_version' ~ '^[1-9][0-9]*$'
 ON CONFLICT DO NOTHING;
CREATE TABLE governance_workspace_legacy_datasets (
 workspace_id text NOT NULL REFERENCES governance_workspaces(id),
 dataset_id text PRIMARY KEY,
 dataset_kind text NOT NULL DEFAULT 'dataset' CHECK(dataset_kind='dataset'),
 FOREIGN KEY(dataset_kind,dataset_id) REFERENCES business_records(kind,id)
);
INSERT INTO governance_workspace_legacy_datasets(workspace_id,dataset_id)
 SELECT md5('governance-workspace:' || b.department_id),b.id FROM business_records b
 WHERE b.kind='dataset' AND NOT EXISTS(SELECT 1 FROM governance_outputs o WHERE o.dataset_id=b.id);
CREATE FUNCTION governance_legacy_dataset_workspace() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
BEGIN
 IF NEW.kind='dataset' AND NOT EXISTS(SELECT 1 FROM public.governance_runs WHERE id=NEW.id) THEN
  INSERT INTO public.governance_workspace_legacy_datasets(workspace_id,dataset_id) VALUES(public.governance_legacy_workspace(NEW.department_id),NEW.id) ON CONFLICT DO NOTHING;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER governance_legacy_dataset_workspace AFTER INSERT ON business_records FOR EACH ROW EXECUTE FUNCTION governance_legacy_dataset_workspace();
ALTER TABLE governance_alert_rules ADD COLUMN workspace_id text;
ALTER TABLE governance_alert_rules ADD CONSTRAINT governance_alert_workspace_department FOREIGN KEY(workspace_id,department_id) REFERENCES governance_workspaces(id,department_id);
GRANT SELECT,INSERT,UPDATE ON governance_workspaces,governance_workspace_sources TO datamesh_app;
GRANT SELECT,INSERT ON governance_workspace_legacy_datasets TO datamesh_app;
