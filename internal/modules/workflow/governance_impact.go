package workflow

import (
	"encoding/json"
	"fmt"
	"slices"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type governanceDependency struct {
	Kind         string `json:"kind"`
	ID           string `json:"id"`
	Version      int    `json:"version"`
	State        string `json:"state"`
	DepartmentID string `json:"-"`
	OwnerID      string `json:"-"`
}

// Dependency checks cover all departments; only authorized identities may be disclosed.
func governanceDependencies(tx *gorm.DB, id string, live bool) ([]governanceDependency, error) {
	out := []governanceDependency{}
	defs := []struct {
		DefinitionID string
		Version      int
		State        string
		DepartmentID string
	}{}
	path := "$.** ? (@.resource_id == $id || @.id == $id)"
	if !live {
		path = "$.** ? (@.resource_id == $id || @.id == $id || @.object_id == $id)"
	}
	q := tx.Table("governance_versions v").Select("v.definition_id,v.version,v.state,d.department_id").Joins("JOIN governance_definitions d ON d.id=v.definition_id").Where("jsonb_path_exists(v.definition, ?::jsonpath, jsonb_build_object('id',?::text))", path, id)
	if live {
		q = q.Where("v.state='published' OR (v.state='draft' AND v.version=d.latest_version)")
	}
	if e := q.Order("v.definition_id,v.version").Limit(1001).Scan(&defs).Error; e != nil {
		return nil, e
	}
	if len(defs) > 1000 {
		return nil, problem(409, "GOVERNANCE_DEPENDENCY_LIMIT", "依赖超过单次核验上限，请缩小范围")
	}
	for _, v := range defs {
		out = append(out, governanceDependency{"definition", v.DefinitionID, v.Version, v.State, v.DepartmentID, ""})
	}
	records := []Record{}
	q = tx.Where(`(kind IN ('mount_revision','grant','processing_authorization','etl_task','etl_run','table_resource','object') AND
 (body->'resource'->>'resource_id'=? OR body->'scope'->'resource'->>'resource_id'=? OR body->'source_resource'->>'resource_id'=? OR body->'definition'->'source_resource'->>'resource_id'=? OR body->>'source_id'=? OR body->'config'->>'source_id'=?)) OR (kind='dataset' AND (body->>'governance_run_id'=? OR body->'definition'->>'source_object_id'=?))
 OR (kind='mount' AND (body->>'active_id'=? OR body->>'draft_id'=?))
 OR (kind='directory' AND EXISTS(SELECT 1 FROM business_records m WHERE m.kind='mount' AND m.id=? AND m.parent_id=business_records.id))
 OR (kind IN ('etl_task','etl_run') AND (body->'source'->>'source_id'=? OR body->'target'->>'source_id'=? OR body->'definition'->'source'->>'source_id'=? OR body->'definition'->'target'->>'source_id'=?))`, id, id, id, id, id, id, id, id, id, id, id, id, id, id, id)
	if live {
		q = q.Where("NOT(kind='dataset' AND id=?)", id)
		q = q.Where(`(kind='mount_revision' AND EXISTS(SELECT 1 FROM business_records m WHERE m.kind='mount' AND ((m.state IN ('active','suspended') AND m.body->>'active_id'=business_records.id) OR (m.body->>'draft_id'=business_records.id AND business_records.state IN ('draft','validated','pending') )))) OR (kind IN ('grant','processing_authorization') AND state='active' AND COALESCE((COALESCE(body->'scope',body)->>'expires_at')::timestamptz,'infinity')>now()) OR (kind='etl_task' AND state<>'archived') OR (kind='etl_run' AND state IN ('queued','extracting','loading','committing','unknown')) OR kind IN ('table_resource','object') OR (kind='dataset' AND state<>'retired')`)
	}
	if e := q.Order("id").Limit(1001).Find(&records).Error; e != nil {
		return nil, e
	}
	if len(records) > 1000 {
		return nil, problem(409, "GOVERNANCE_DEPENDENCY_LIMIT", "依赖超过单次核验上限，请缩小范围")
	}
	for _, v := range records {
		out = append(out, governanceDependency{v.Kind, v.ID, v.Version, v.State, v.DepartmentID, v.OwnerID})
	}
	runs := []GovernanceRun{}
	q = tx.Where("id<>? AND (EXISTS(SELECT 1 FROM jsonb_each(COALESCE(definition->'inputs','{}')) i WHERE i.value->'resource'->>'resource_id'=?) OR definition->'input'->'resource'->>'resource_id'=?)", id, id, id)
	if live {
		q = q.Where("state IN ('queued','running') OR (state='succeeded' AND EXISTS(SELECT 1 FROM governance_outputs o WHERE o.run_id=governance_runs.id) AND NOT EXISTS(SELECT 1 FROM governance_snapshot_purges p WHERE p.run_id=governance_runs.id))")
	}
	if e := q.Order("id").Limit(1001).Find(&runs).Error; e != nil {
		return nil, e
	}
	if len(runs) > 1000 {
		return nil, problem(409, "GOVERNANCE_DEPENDENCY_LIMIT", "运行依赖超过单次核验上限")
	}
	for _, v := range runs {
		out = append(out, governanceDependency{"run", v.ID, 1, v.State, v.DepartmentID, v.ActorID})
	}
	parents := []GovernanceRun{}
	if e := tx.Where("kind='workflow' AND EXISTS(SELECT 1 FROM governance_workflow_tasks t WHERE t.parent_run_id=governance_runs.id AND t.child_run_id=?)", id).Limit(1001).Find(&parents).Error; e != nil {
		return nil, e
	}
	if len(parents) > 1000 {
		return nil, problem(409, "GOVERNANCE_DEPENDENCY_LIMIT", "工作流依赖超过核验上限")
	}
	for _, v := range parents {
		if !live || slices.Contains([]string{"queued", "running"}, v.State) {
			out = append(out, governanceDependency{"run", v.ID, 1, v.State, v.DepartmentID, v.ActorID})
		}
	}
	return out, nil
}
func governanceDependencyVisible(tx *gorm.DB, p identity.Principal, d governanceDependency) bool {
	if admin(p) {
		return true
	}
	if p.DepartmentID != d.DepartmentID {
		return false
	}
	if d.Kind == "definition" {
		return true
	}
	if d.Kind == "run" {
		return p.UserID == d.OwnerID
	}
	if slices.Contains([]string{"object", "table_resource", "file_resource", "api_resource", "dataset"}, d.Kind) {
		return requireResourceCapability(tx, p, Resource{Type: d.Kind, ID: d.ID}, "govern_resource") == nil
	}
	return p.UserID == d.OwnerID
}
func (s *Server) governanceImpactGraph(c *gin.Context) {
	tx := s.db.WithContext(c.Request.Context())
	p := principal(c)
	id := c.Query("id")
	kind := c.Query("kind")
	if !valid(id, 128) {
		s.fail(c, invalid)
		return
	}
	root := governanceDependency{Kind: kind, ID: id, Version: 1}
	switch kind {
	case "definition":
		var head GovernanceDefinitionRecord
		if e := tx.Where("id=?", id).Take(&head).Error; e != nil {
			s.fail(c, e)
			return
		}
		h, v, e := governanceRead(tx, p, head.Kind, id, 0)
		if e != nil {
			s.fail(c, e)
			return
		}
		root.Version = v.Version
		root.State = v.State
		root.DepartmentID = h.DepartmentID
	case "run":
		r, e := governanceRunRead(tx, p, id, false)
		if e != nil {
			s.fail(c, e)
			return
		}
		root.State = r.State
		root.DepartmentID = r.DepartmentID
		root.OwnerID = r.ActorID
	case "source":
		r, e := get(tx, "source", id, false)
		if e != nil {
			s.fail(c, e)
			return
		}
		if !department(p, r) || requireResourceCapability(tx, p, Resource{Type: "source", ID: id}, "govern_resource") != nil {
			s.fail(c, denied)
			return
		}
		root.State = r.State
		root.Version = r.Version
	case "resource":
		r, _, e := metadataTarget(tx, p, id, false)
		if e != nil {
			s.fail(c, e)
			return
		}
		root.Kind = r.Kind
		root.State = r.State
		root.Version = r.Version
	default:
		s.fail(c, invalid)
		return
	}
	key := func(d governanceDependency) string { return fmt.Sprintf("%s:%s:%d", d.Kind, d.ID, d.Version) }
	nodes := []governanceDependency{root}
	edges := []gin.H{}
	seen := map[string]bool{key(root): true}
	truncated := false
	hidden := 0
	for cursor := 0; cursor < len(nodes) && cursor < 100; cursor++ {
		n := nodes[cursor]
		deps, e := governanceDependencies(tx, n.ID, false)
		if e != nil {
			s.fail(c, e)
			return
		}
		for _, d := range deps {
			if !governanceDependencyVisible(tx, p, d) {
				hidden++
				continue
			}
			k := key(d)
			if k == key(n) {
				continue
			}
			if !seen[k] {
				if len(nodes) >= 100 {
					truncated = true
					continue
				}
				seen[k] = true
				nodes = append(nodes, d)
			}
			if len(edges) >= 500 {
				truncated = true
				break
			}
			edges = append(edges, gin.H{"source": key(n), "target": k, "relation": "depends_on"})
		}
	}
	// Field lineage is immutable execution evidence; no source configuration or row values are returned.
	fields := json.RawMessage("null")
	fieldLabels := map[string]string{}
	graph := json.RawMessage("null")
	if kind == "run" {
		r, e := governanceRunRead(tx, p, id, false)
		if e != nil {
			s.fail(c, e)
			return
		}
		if r.Kind == "dataflow" {
			d, e := decode[governanceFlowExecution](r.Definition)
			if e != nil {
				s.fail(c, e)
				return
			}
			actor, er := s.etlPrincipal(c.Request.Context(), r.ActorID)
			if er != nil {
				s.fail(c, denied)
				return
			}
			if e = s.governanceFlowAuthorize(c.Request.Context(), actor, d); e != nil {
				s.fail(c, e)
				return
			}
			v, e := decode[governanceFlowReport](r.Result)
			if e != nil {
				s.fail(c, e)
				return
			}
			fields = encode(v.Lineage)
			fieldLabels = v.FieldLabels
			if len(d.Pinned) > 0 && d.Pinned[0].Definition.Dataflow != nil {
				g := *d.Pinned[0].Definition.Dataflow
				for i := range g.Nodes {
					if g.Nodes[i].Source != nil {
						src := *g.Nodes[i].Source
						src.Parameters = nil
						src.ParameterBindings = nil
						g.Nodes[i].Source = &src
					}
					if g.Nodes[i].Condition != nil {
						v := *g.Nodes[i].Condition
						v.Value = nil
						g.Nodes[i].Condition = &v
					}
				}
				graph = encode(g)
			}
		}
	}
	c.JSON(200, gin.H{"root": key(root), "nodes": nodes, "edges": edges, "fields": fields, "field_labels": fieldLabels, "transformations": graph, "truncated": truncated, "restricted_dependencies": hidden, "max_nodes": 100, "max_edges": 500})
}
