package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type governanceCodePreview struct {
	NodeID      string `json:"node_id"`
	SampleLimit int    `json:"sample_limit"`
}
type governanceCodePreviewInput struct {
	Definition  GovernanceDefinition `json:"definition"`
	NodeID      string               `json:"node_id"`
	SampleLimit int                  `json:"sample_limit"`
}

func compileGovernancePreviewPrefix(graph GovernanceDataflow, target string) (GovernanceDataflow, error) {
	nodes := map[string]GovernanceFlowNode{}
	for _, n := range graph.Nodes {
		if nodes[n.ID].ID != "" {
			return GovernanceDataflow{}, invalid
		}
		nodes[n.ID] = n
	}
	if nodes[target].Kind != "script" {
		return GovernanceDataflow{}, invalid
	}
	seen, visiting := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return flowProblem("GOVERNANCE_GRAPH_CYCLE", "上游存在循环连线")
		}
		if seen[id] {
			return nil
		}
		n, ok := nodes[id]
		if !ok {
			return invalid
		}
		visiting[id] = true
		for _, edge := range n.Inputs {
			if e := visit(edge.Node); e != nil {
				return e
			}
		}
		delete(visiting, id)
		seen[id] = true
		return nil
	}
	if e := visit(target); e != nil {
		return GovernanceDataflow{}, e
	}
	result := GovernanceDataflow{Nodes: []GovernanceFlowNode{}}
	for _, n := range graph.Nodes {
		if seen[n.ID] {
			result.Nodes = append(result.Nodes, n)
		}
	}
	return result, nil
}
func (s *Server) registerGovernanceCodePreview(api, write *gin.RouterGroup) {
	write.POST("/governance/dataflows/code-preview", s.require("governance", "governor"), s.mutate("governance_code_preview", func(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
		input, e := bind[governanceCodePreviewInput](b)
		if e != nil {
			return 0, nil, "", e
		}
		return s.enqueueGovernanceCodePreview(tx, p, input, nil)
	}))
	api.GET("/governance/runs/:id/code-preview-records", s.require("governance", "governor"), s.governanceCodePreviewRecords)
}
func (s *Server) enqueueGovernanceCodePreview(tx *gorm.DB, p identity.Principal, input governanceCodePreviewInput, retry *string) (int, json.RawMessage, string, error) {
	space, spaceErr := governanceWorkspaceCreateScope(tx, p, p.DepartmentID)
	if spaceErr != nil {
		return 0, nil, "", spaceErr
	}

	if input.Definition.Dataflow == nil || input.SampleLimit != 20 {
		return 0, nil, "", invalid
	}
	graph, e := compileGovernancePreviewPrefix(*input.Definition.Dataflow, input.NodeID)
	if e != nil {
		return 0, nil, "", e
	}
	input.Definition.Dataflow = &graph
	if e := governanceValidateReferencesForPreview(tx, p, p.DepartmentID, space.ID, input.Definition, input.NodeID); e != nil {
		return 0, nil, "", e
	}
	execution := governanceFlowExecution{Preview: &governanceCodePreview{input.NodeID, 20}, Pinned: []governanceFlowPinned{{Kind: "dataflow", Definition: input.Definition}}, Inputs: map[string]governanceInput{}}
	seen := map[governanceReference]bool{}
	queue := governanceReferences(input.Definition)
	for len(queue) > 0 {
		r := queue[0]
		queue = queue[1:]
		if seen[r] {
			continue
		}
		seen[r] = true
		_, version, e := governanceRead(tx, p, r.Kind, r.Ref.ID, r.Ref.Version)
		if e != nil {
			return 0, nil, "", e
		}
		if version.State != "published" {
			return 0, nil, "", conflict
		}
		definition, e := decode[GovernanceDefinition](version.Definition)
		if e != nil {
			return 0, nil, "", e
		}
		execution.Pinned = append(execution.Pinned, governanceFlowPinned{r.Kind, r.Ref, definition})
		queue = append(queue, governanceReferences(definition)...)
		if len(seen) > 200 {
			return 0, nil, "", invalid
		}
	}
	for _, node := range graph.Nodes {
		if node.Source == nil {
			continue
		}
		basis, e := s.governanceInputBasisParameters(tx, p, node.Source.Resource, node.Source.Fields, true, node.Source.Parameters)
		if e != nil {
			return 0, nil, "", e
		}
		for _, field := range basis.Fields {
			if !admin(p) && field.Sensitive && field.LogicalType != "string" {
				return 0, nil, "", problem(403, "GOVERNANCE_MASKED_TYPE", "当前授权不允许使用敏感数值或日期参与计算")
			}
		}
		execution.Inputs[node.ID] = basis
	}
	if e = s.validateGovernanceScriptRuntimes(tx.Statement.Context, graph); e != nil {
		return 0, nil, "", e
	}
	if _, _, _, e = governanceExecutionPlan(execution); e != nil {
		return 0, nil, "", e
	}
	if e = tx.Exec("SELECT pg_advisory_xact_lock(619483219)").Error; e != nil {
		return 0, nil, "", e
	}
	var pending int64
	if e = tx.Model(&GovernanceRun{}).Where("actor_id=? AND state IN ('queued','running')", p.UserID).Count(&pending).Error; e != nil {
		return 0, nil, "", e
	}
	if pending >= 10 {
		return 0, nil, "", conflict
	}
	id, e := newID()
	if e != nil {
		return 0, nil, "", e
	}
	run := GovernanceRun{ID: id, WorkspaceID: space.ID, Kind: "dataflow_preview", DepartmentID: p.DepartmentID, ActorID: p.UserID, Definition: encode(execution), State: "queued", Result: json.RawMessage("null"), RetryOf: retry, LeaseExpiresAt: time.Unix(0, 0).UTC()}
	if e = tx.Create(&run).Error; e != nil {
		return 0, nil, "", e
	}
	if e = governanceRunEvent(tx, run, "CODE_PREVIEW_QUEUED"); e != nil {
		return 0, nil, "", e
	}
	return 201, encode(run), id, nil
}
func (s *Server) finishGovernanceCodePreview(ctx context.Context, tx *sql.Tx, p identity.Principal, run GovernanceRun, def governanceFlowExecution, plan governanceFlowPlan, report governanceFlowReport, processed int64, guard func() error) (json.RawMessage, int64, error) {
	target := def.Preview.NodeID
	var node GovernanceFlowNode
	for _, n := range def.Pinned[0].Definition.Dataflow.Nodes {
		if n.ID == target {
			node = n
		}
	}
	pairs := []struct {
		edge GovernanceFlowEdge
		port string
	}{{node.Inputs[0], "preview_input"}, {GovernanceFlowEdge{Node: target}, "preview_output"}}
	for _, pair := range pairs {
		rows, e := tx.QueryContext(ctx, "SELECT ordinal,payload FROM governance_flow_stage WHERE node_id=$1 AND port=$2 ORDER BY ordinal LIMIT 20", pair.edge.Node, pair.edge.Port)
		if e != nil {
			return nil, processed, e
		}
		type sample struct {
			ordinal int64
			body    []byte
		}
		samples := []sample{}
		for rows.Next() {
			var item sample
			if e = rows.Scan(&item.ordinal, &item.body); e != nil {
				rows.Close()
				return nil, processed, e
			}
			samples = append(samples, item)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, processed, e
		}
		for _, item := range samples {
			row, e := decode[DataRow](item.body)
			if e != nil {
				return nil, processed, e
			}
			if !admin(p) {
				for _, field := range plan.Outputs[pair.edge] {
					if field.Field.Sensitive && string(row[field.Field.Name]) != "null" {
						row[field.Field.Name] = encode("***")
					}
				}
			}
			cipher, e := s.runtime.cryptScope(encode(row), true, governanceFlowScope(run.ID, target, pair.port, item.ordinal))
			if e != nil {
				return nil, processed, e
			}
			if _, e = tx.ExecContext(ctx, "INSERT INTO governance_flow_rows(run_id,node_id,port,ordinal,encrypted_body) VALUES($1,$2,$3,$4,$5)", run.ID, target, pair.port, item.ordinal, cipher); e != nil {
				return nil, processed, e
			}
		}
	}
	if e := guard(); e != nil {
		return nil, processed, e
	}
	report.DatasetID = ""
	payload := encode(report)
	result, e := tx.ExecContext(ctx, "UPDATE governance_runs SET state='succeeded',result=$1::jsonb,processed=$2,finished_at=now(),updated_at=now() WHERE id=$3 AND state='running' AND lease_owner=$4 AND lease_expires_at>now() AND cancel_requested=false", string(payload), processed, run.ID, run.LeaseOwner)
	if e != nil {
		return nil, processed, e
	}
	affected, e := result.RowsAffected()
	if e != nil {
		return nil, processed, e
	}
	if affected != 1 {
		return nil, processed, conflict
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO governance_run_events(run_id,code,processed) VALUES($1,'CODE_PREVIEW_COMPLETED',$2)", run.ID, processed); e != nil {
		return nil, processed, e
	}
	if e = tx.Commit(); e != nil {
		return nil, processed, e
	}
	return payload, processed, nil
}
func (s *Server) finishGovernanceDorisCodePreview(ctx context.Context, stage *governanceDorisStage, p identity.Principal, run GovernanceRun, def governanceFlowExecution, plan governanceFlowPlan, report governanceFlowReport, processed int64, guard func() error) (json.RawMessage, int64, error) {
	db, err := s.db.DB()
	if err != nil {
		return nil, processed, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, processed, err
	}
	defer tx.Rollback()
	target := def.Preview.NodeID
	var node GovernanceFlowNode
	for _, n := range def.Pinned[0].Definition.Dataflow.Nodes {
		if n.ID == target {
			node = n
		}
	}
	pairs := []struct {
		edge GovernanceFlowEdge
		port string
	}{{node.Inputs[0], "preview_input"}, {GovernanceFlowEdge{Node: target}, "preview_output"}}
	for _, pair := range pairs {
		rows, e := stage.QueryContext(ctx, "SELECT ordinal,payload FROM governance_flow_stage WHERE node_id=$1 AND port=$2 ORDER BY ordinal LIMIT 20", pair.edge.Node, pair.edge.Port)
		if e != nil {
			return nil, processed, e
		}
		type sample struct {
			ordinal int64
			body    []byte
		}
		samples := []sample{}
		for rows.Next() {
			var item sample
			if e = rows.Scan(&item.ordinal, &item.body); e != nil {
				rows.Close()
				return nil, processed, e
			}
			samples = append(samples, item)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, processed, e
		}
		for _, item := range samples {
			row, e := decode[DataRow](item.body)
			if e != nil {
				return nil, processed, e
			}
			if !admin(p) {
				for _, field := range plan.Outputs[pair.edge] {
					if field.Field.Sensitive && string(row[field.Field.Name]) != "null" {
						row[field.Field.Name] = encode("***")
					}
				}
			}
			cipher, e := s.runtime.cryptScope(encode(row), true, governanceFlowScope(run.ID, target, pair.port, item.ordinal))
			if e != nil {
				return nil, processed, e
			}
			if _, e = tx.ExecContext(ctx, "INSERT INTO governance_flow_rows(run_id,node_id,port,ordinal,encrypted_body) VALUES($1,$2,$3,$4,$5)", run.ID, target, pair.port, item.ordinal, cipher); e != nil {
				return nil, processed, e
			}
		}
	}
	if e := guard(); e != nil {
		return nil, processed, e
	}
	report.DatasetID = ""
	payload := encode(report)
	result, e := tx.ExecContext(ctx, "UPDATE governance_runs SET state='succeeded',result=$1::jsonb,processed=$2,finished_at=now(),updated_at=now() WHERE id=$3 AND state='running' AND lease_owner=$4 AND lease_expires_at>now() AND cancel_requested=false", string(payload), processed, run.ID, run.LeaseOwner)
	if e != nil {
		return nil, processed, e
	}
	affected, e := result.RowsAffected()
	if e != nil {
		return nil, processed, e
	}
	if affected != 1 {
		return nil, processed, conflict
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO governance_run_events(run_id,code,processed) VALUES($1,'CODE_PREVIEW_COMPLETED',$2)", run.ID, processed); e != nil {
		return nil, processed, e
	}
	if e = tx.Commit(); e != nil {
		return nil, processed, e
	}
	return payload, processed, nil
}
func (s *Server) governanceCodePreviewRecords(c *gin.Context) {
	ctx := c.Request.Context()
	p := principal(c)
	run, e := governanceRunRead(s.db.WithContext(ctx), p, c.Param("id"), false)
	if e != nil {
		s.fail(c, e)
		return
	}
	if run.Kind != "dataflow_preview" || run.State != "succeeded" {
		s.fail(c, conflict)
		return
	}
	def, e := decode[governanceFlowExecution](run.Definition)
	if e != nil || def.Preview == nil {
		s.fail(c, invalid)
		return
	}
	actor, e := s.etlPrincipal(ctx, run.ActorID)
	if e != nil {
		s.fail(c, denied)
		return
	}
	if e = s.governanceFlowAuthorize(ctx, actor, def); e != nil {
		s.fail(c, e)
		return
	}
	var stored []struct {
		Ordinal       int64
		Port          string
		EncryptedBody []byte
	}
	if e = s.db.WithContext(ctx).Table("governance_flow_rows").Where("run_id=? AND node_id=? AND port IN ('preview_input','preview_output')", run.ID, def.Preview.NodeID).Order("port,ordinal").Limit(40).Find(&stored).Error; e != nil {
		s.fail(c, e)
		return
	}
	input, output := []DataRow{}, []DataRow{}
	for _, item := range stored {
		body, e := s.runtime.cryptScope(item.EncryptedBody, false, governanceFlowScope(run.ID, def.Preview.NodeID, item.Port, item.Ordinal))
		if e != nil {
			s.fail(c, conflict)
			return
		}
		row, e := decode[DataRow](body)
		if e != nil {
			s.fail(c, e)
			return
		}
		if item.Port == "preview_input" {
			input = append(input, row)
		} else {
			output = append(output, row)
		}
	}
	if e = s.governanceFlowAuthorize(ctx, actor, def); e != nil {
		s.fail(c, e)
		return
	}
	c.JSON(200, gin.H{"input": input, "output": output, "sample_limit": 20, "scope": "来源顺序样本；每个来源最多20行，仅展示目标节点前20行，不代表全量结果"})
}
