package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type governanceFlowPinned struct {
	Kind       string               `json:"kind"`
	Ref        GovernanceRef        `json:"ref"`
	Definition GovernanceDefinition `json:"definition"`
}
type governanceFlowExecution struct {
	WorkflowTrial  *governanceTrial                `json:"workflow_trial,omitempty"`
	Trial          *governanceTrial                `json:"trial,omitempty"`
	Lightweight    *governanceLightweightExecution `json:"lightweight,omitempty"`
	ExecutionPlan  *GovernanceExecutionPlan        `json:"execution_plan,omitempty"`
	StreamSnapshot *governanceStreamSnapshot       `json:"stream_snapshot,omitempty"`
	Preview        *governanceCodePreview          `json:"preview,omitempty"`
	Parameters     map[string]json.RawMessage      `json:"parameters,omitempty"`
	Flow           GovernanceRef                   `json:"flow"`
	Pinned         []governanceFlowPinned          `json:"pinned"`
	Inputs         map[string]governanceInput      `json:"inputs"`
}
type governanceFlowNodeResult struct {
	NodeID        string `json:"node_id"`
	InputCount    int64  `json:"input_count"`
	OutputCount   int64  `json:"output_count"`
	RejectedCount int64  `json:"rejected_count"`
	DurationMS    int64  `json:"duration_ms"`
}
type governanceFlowReport struct {
	Storage             *GovernanceStorage                 `json:"storage,omitempty"`
	DataLayer           string                             `json:"data_layer,omitempty"`
	Engine              string                             `json:"engine,omitempty"`
	GeneratedModel      *GovernanceRef                     `json:"generated_model,omitempty"`
	FieldLabels         map[string]string                  `json:"field_labels,omitempty"`
	Unmodeled           bool                               `json:"unmodeled,omitempty"`
	ConservativeLineage bool                               `json:"conservative_lineage,omitempty"`
	DatasetID           string                             `json:"dataset_id,omitempty"`
	Flow                GovernanceRef                      `json:"flow"`
	Model               GovernanceRef                      `json:"model"`
	OutputNode          string                             `json:"output_node"`
	OutputRows          int64                              `json:"output_rows"`
	RejectedRows        int64                              `json:"rejected_rows"`
	SnapshotScope       string                             `json:"snapshot_scope"`
	Nodes               []governanceFlowNodeResult         `json:"nodes"`
	Lineage             map[string][]GovernanceFieldOrigin `json:"lineage"`
}

func (s *Server) registerGovernanceDataflows(api, write *gin.RouterGroup) {
	s.registerLightweightGovernance(api, write)
	s.registerDataSandboxes(api, write)
	s.registerGovernanceStreams(api, write)
	s.registerGovernanceExecutionPlan(api, write)
	s.registerGovernanceExports(api, write)
	s.registerGovernanceFlowLayouts(api, write)
	s.registerGovernanceCode(api, write)
	api.GET("/governance/runs/:id/model-binding", s.require("governance", "governor"), s.governanceResultModelBinding)
	api.GET("/governance/runs/:id/model-proposal", s.require("governance", "governor"), s.governanceResultModelProposal)
	write.POST("/governance/runs/:id/model-draft", s.require("governance", "governor"), s.mutatePath("governance_create_result_model", s.governanceSaveResultModel))
	write.POST("/governance/dataflows/:id/versions/:version/run", s.require("governance", "governor"), func(c *gin.Context) {
		version, err := intPath(c, "version")
		if err != nil {
			s.fail(c, err)
			return
		}
		id := c.Param("id")
		s.mutate("governance_run_dataflow", func(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
			return s.enqueueGovernanceFlow(tx, p, GovernanceRef{ID: id, Version: version}, b)
		})(c)
	})
	api.GET("/governance/runs/:id/records", s.require("governance", "governor"), s.governanceFlowRecords)
}
func intPath(c *gin.Context, key string) (int, error) {
	var n int
	if _, e := fmt.Sscanf(c.Param(key), "%d", &n); e != nil || n < 1 || fmt.Sprint(n) != c.Param(key) {
		return 0, invalid
	}
	return n, nil
}
func (s *Server) enqueueGovernanceFlow(tx *gorm.DB, p identity.Principal, ref GovernanceRef, b []byte) (int, json.RawMessage, string, error) {
	return s.enqueueGovernanceFlowBindings(tx, p, ref, b, nil, "")
}
func (s *Server) enqueueGovernanceFlowBindings(tx *gorm.DB, p identity.Principal, ref GovernanceRef, b []byte, bindings map[string]string, parent string) (int, json.RawMessage, string, error) {
	prepared, e := s.prepareGovernanceFlow(tx, p, ref, b, bindings, parent)
	if e != nil {
		return 0, nil, ref.ID, e
	}
	if e = requireGovernanceFlowMode(prepared.Mode, "batch"); e != nil {
		return 0, nil, ref.ID, e
	}
	execution := prepared.Execution
	plan := governanceEnginePlan(execution, "batch")
	execution.ExecutionPlan = &plan

	var pending int64
	if e = tx.Model(&GovernanceRun{}).Where("actor_id=? AND kind<>'workflow' AND state IN ('queued','running')", p.UserID).Count(&pending).Error; e != nil {
		return 0, nil, ref.ID, e
	}
	if pending >= 10 {
		return 0, nil, ref.ID, conflict
	}
	id, e := newID()
	if e != nil {
		return 0, nil, ref.ID, e
	}
	if execution.Trial != nil {
		execution.Trial.RunID = id
	}
	run := GovernanceRun{ID: id, WorkspaceID: prepared.WorkspaceID, Kind: "dataflow", DepartmentID: prepared.DepartmentID, ActorID: p.UserID, Definition: encode(execution), State: "queued", Result: json.RawMessage("null"), LeaseExpiresAt: time.Unix(0, 0).UTC()}
	if e = tx.Create(&run).Error; e != nil {
		return 0, nil, ref.ID, e
	}
	if e = governanceRunEvent(tx, run, "FLOW_QUEUED"); e != nil {
		return 0, nil, ref.ID, e
	}
	return 201, encode(run), run.ID, nil
}

type governancePreparedFlow struct {
	Mode         string
	Execution    governanceFlowExecution
	WorkspaceID  string
	DepartmentID string
}

func (s *Server) prepareGovernanceFlow(tx *gorm.DB, p identity.Principal, ref GovernanceRef, b []byte, bindings map[string]string, parent string) (governancePreparedFlow, error) {
	values, parameterErr := bind[governanceFlowRunInput](b)
	if parameterErr != nil {
		return governancePreparedFlow{}, parameterErr
	}
	head, row, e := governanceRead(tx, p, "dataflow", ref.ID, ref.Version)
	if e != nil {
		return governancePreparedFlow{}, e
	}
	space, scopeErr := governanceWorkspaceWrite(tx, p, workspaceID(head.WorkspaceID))
	if scopeErr != nil {
		return governancePreparedFlow{}, scopeErr
	}
	var trial *governanceTrial
	if request, ok := tx.Statement.Context.Value(governanceTrialContextKey{}).(governanceTrial); ok {
		if row.State != "draft" || head.LatestVersion != ref.Version {
			return governancePreparedFlow{}, conflict
		}
		if e = governanceValidateTrial(tx, p, request, ref, row.Definition, true); e != nil {
			return governancePreparedFlow{}, e
		}
		trial = &request
	} else if row.State != "published" {
		return governancePreparedFlow{}, conflict
	}
	definition, e := decode[GovernanceDefinition](row.Definition)
	if e != nil {
		return governancePreparedFlow{}, e
	}
	if definition.Dataflow == nil {
		return governancePreparedFlow{}, invalid
	}
	if e = governanceValidateReferences(tx, p, head.DepartmentID, workspaceID(head.WorkspaceID), definition); e != nil {
		return governancePreparedFlow{}, e
	}
	execution := governanceFlowExecution{Trial: trial, Flow: ref, Pinned: []governanceFlowPinned{{Kind: "dataflow", Ref: ref, Definition: definition}}, Inputs: map[string]governanceInput{}, Parameters: values.Parameters}
	if request, ok := tx.Statement.Context.Value(governanceWorkflowTrialContextKey{}).(governanceTrial); ok {
		if parent != request.RunID {
			return governancePreparedFlow{}, denied
		}
		if e = governanceWorkflowTrialValidate(tx, p, request); e != nil {
			return governancePreparedFlow{}, e
		}
		execution.WorkflowTrial = &request
	}
	seen := map[governanceReference]bool{}
	queue := governanceReferences(definition)
	for len(queue) > 0 {
		r := queue[0]
		queue = queue[1:]
		if seen[r] {
			continue
		}
		seen[r] = true
		_, version, e := governanceRead(tx, p, r.Kind, r.Ref.ID, r.Ref.Version)
		if e != nil {
			return governancePreparedFlow{}, e
		}
		v, e := decode[GovernanceDefinition](version.Definition)
		if e != nil {
			return governancePreparedFlow{}, e
		}
		execution.Pinned = append(execution.Pinned, governanceFlowPinned{r.Kind, r.Ref, v})
		queue = append(queue, governanceReferences(v)...)
		if len(execution.Pinned) > 200 {
			return governancePreparedFlow{}, problem(409, "GOVERNANCE_REFERENCE_LIMIT", "引用依赖超过治理上限")
		}
	}
	graph, e := governanceMaterializeFlow(*definition.Dataflow, values.Parameters, nil)
	if e != nil {
		return governancePreparedFlow{}, e
	}
	for name := range bindings {
		if !slices.ContainsFunc(graph.Nodes, func(node GovernanceFlowNode) bool { return node.ID == name && node.Kind == "source" }) {
			return governancePreparedFlow{}, invalid
		}
	}
	for _, node := range graph.Nodes {
		if node.Source == nil {
			continue
		}
		var input governanceInput
		if child, ok := bindings[node.ID]; ok {
			input, e = s.governanceWorkflowArtifactBasis(tx, p, parent, child, node.Source.Fields)
			if e == nil {
				_, expected, err := metadataTarget(tx, p, node.Source.Resource.ID, false)
				if err != nil {
					return governancePreparedFlow{}, err
				}
				if !governanceInputFieldsCompatible(input.Fields, expected.Fields) {
					return governancePreparedFlow{}, problem(400, "WORKFLOW_ARTIFACT_SCHEMA", "前置成果与绑定输入字段类型不兼容")
				}
			}
		} else {
			input, e = s.governanceInputBasisParameters(tx, p, node.Source.Resource, node.Source.Fields, true, node.Source.Parameters)
		}
		if e != nil {
			return governancePreparedFlow{}, e
		}
		if !admin(p) && slices.ContainsFunc(input.Fields, func(f Field) bool { return f.Sensitive && f.LogicalType != "string" }) {
			return governancePreparedFlow{}, problem(403, "GOVERNANCE_MASKED_TYPE", "数值或日期字段需要脱敏，当前授权不足以保持其类型参与治理，请调整字段范围或授权")
		}
		execution.Inputs[node.ID] = input
	}
	if e = s.validateGovernanceScriptRuntimes(tx.Statement.Context, graph); e != nil {
		return governancePreparedFlow{}, e
	}
	if _, _, _, e = governanceExecutionPlan(execution); e != nil {
		return governancePreparedFlow{}, e
	}
	if e = tx.Exec("SELECT pg_advisory_xact_lock(619483219)").Error; e != nil {
		return governancePreparedFlow{}, e
	}
	// Read purge evidence in the dispatch transaction: predecessor task state can
	// have become succeeded here before the orchestrator transaction commits.
	for _, input := range execution.Inputs {
		if input.Resource.Type != "governed_dataset" {
			continue
		}
		var purged int64
		if e = tx.Table("governance_snapshot_purges").Where("run_id=?", input.Resource.ID).Count(&purged).Error; e != nil {
			return governancePreparedFlow{}, e
		}
		if purged > 0 {
			return governancePreparedFlow{}, problem(410, "GOVERNANCE_SNAPSHOT_PURGED", "来源成果快照已清理，请重新选择输入")
		}
	}

	return governancePreparedFlow{Mode: row.ExecutionMode, Execution: execution, WorkspaceID: space.ID, DepartmentID: head.DepartmentID}, nil
}

func (s *Server) governanceFlowAuthorize(ctx context.Context, p identity.Principal, def governanceFlowExecution) error {
	if def.WorkflowTrial != nil {
		if e := governanceWorkflowTrialValidate(s.db.WithContext(governanceReadContext(ctx)), p, *def.WorkflowTrial); e != nil {
			return e
		}
	}
	ctx = context.WithValue(ctx, governanceWorkspaceContextKey{}, governanceWorkspaceScope{})
	if def.Lightweight != nil {
		return s.authorizeLightweightFlow(ctx, p, def)
	}
	for _, input := range def.Inputs {
		if input.UploadID != "" {
			return denied
		}
		if e := s.governanceCheckInput(ctx, p, input, true); e != nil {
			return e
		}
	}
	for index, pinned := range def.Pinned {
		if index == 0 && def.Preview != nil {
			continue
		}
		_, row, e := governanceRead(s.db.WithContext(ctx), p, pinned.Kind, pinned.Ref.ID, pinned.Ref.Version)
		if e != nil {
			return e
		}
		current, e := decode[GovernanceDefinition](row.Definition)
		if e != nil {
			return e
		}
		if index == 0 && def.Trial != nil {
			if e = governanceValidateTrial(s.db.WithContext(ctx), p, *def.Trial, def.Flow, row.Definition, false); e != nil {
				return e
			}
			continue
		}
		if row.State != "published" || fingerprint(encode(current)) != fingerprint(encode(pinned.Definition)) {
			return problem(409, "GOVERNANCE_REFERENCE_UNAVAILABLE", "引用版本已停用或发生变化")
		}
	}
	return nil
}
func (s *Server) governanceFlowRecords(c *gin.Context) {
	ctx := c.Request.Context()
	p := principal(c)
	run, e := governanceRunRead(s.db.WithContext(ctx), p, c.Param("id"), false)
	if e != nil {
		s.fail(c, e)
		return
	}
	var purged int64
	if e = s.db.WithContext(ctx).Table("governance_snapshot_purges").Where("run_id=?", run.ID).Count(&purged).Error; e != nil {
		s.fail(c, e)
		return
	}
	if purged > 0 {
		s.fail(c, problem(410, "GOVERNANCE_SNAPSHOT_PURGED", "成果快照已清理，历史执行和血缘证据仍保留"))
		return
	}
	if run.Kind != "dataflow" || run.State != "succeeded" {
		s.fail(c, conflict)
		return
	}
	def, e := decode[governanceFlowExecution](run.Definition)
	if e != nil {
		s.fail(c, e)
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
	report, e := decode[governanceFlowReport](run.Result)
	if e != nil {
		s.fail(c, e)
		return
	}
	page, e := intQuery(c, "page", 1, 100000)
	if e != nil {
		s.fail(c, e)
		return
	}
	size, e := intQuery(c, "page_size", 20, 100)
	if e != nil {
		s.fail(c, e)
		return
	}
	if report.Storage != nil {
		_, plan, _, err := governanceExecutionPlan(def)
		if err != nil {
			s.fail(c, err)
			return
		}
		fields := []Field{}
		for _, f := range plan.Outputs[GovernanceFlowEdge{Node: report.OutputNode}] {
			fields = append(fields, f.Field)
		}
		items, err := dorisResultPage(ctx, report.Storage, run.ID, fields, (page-1)*size, size)
		if err != nil {
			s.fail(c, err)
			return
		}
		if err = s.governanceFlowAuthorize(ctx, actor, def); err != nil {
			s.fail(c, err)
			return
		}
		c.JSON(200, gin.H{"items": items, "total": report.OutputRows, "page": page, "page_size": size, "model": report.Model, "storage": report.Storage})
		return
	}
	var records []struct {
		Ordinal       int64
		EncryptedBody []byte
	}
	if e = s.db.WithContext(ctx).Table("governance_flow_rows").Where("run_id=? AND node_id=? AND port=''", run.ID, report.OutputNode).Order("ordinal").Limit(size).Offset((page - 1) * size).Find(&records).Error; e != nil {
		s.fail(c, e)
		return
	}
	items := []DataRow{}
	for _, record := range records {
		plain, e := s.runtime.cryptScope(record.EncryptedBody, false, governanceFlowScope(run.ID, report.OutputNode, "", record.Ordinal))
		if e != nil {
			s.fail(c, problem(409, "GOVERNANCE_OUTPUT_INVALID", "结果快照无法校验"))
			return
		}
		row, e := decode[DataRow](plain)
		if e != nil {
			s.fail(c, e)
			return
		}
		items = append(items, row)
	}
	// Authorization can change while encrypted pages are being read.
	if e = s.governanceFlowAuthorize(ctx, actor, def); e != nil {
		s.fail(c, e)
		return
	}
	c.JSON(200, gin.H{"items": items, "total": report.OutputRows, "page": page, "page_size": size, "model": report.Model})
}
func governanceFlowScope(run, node, port string, ordinal int64) string {
	return fmt.Sprintf("datamesh-governance:%s:%s:%s:%d", run, node, port, ordinal)
}
