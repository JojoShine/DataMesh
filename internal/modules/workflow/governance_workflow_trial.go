package workflow

import (
	"bytes"
	"context"
	"datamesh/internal/modules/identity"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type governanceWorkflowTrialContextKey struct{}

func governanceWorkflowTrialValidate(tx *gorm.DB, p identity.Principal, trial governanceTrial) error {
	var root GovernanceRun
	if trial.RunID == "" {
		return invalid
	}
	if e := tx.Where("id=? AND kind='workflow' AND actor_id=? AND department_id=?", trial.RunID, p.UserID, p.DepartmentID).Take(&root).Error; e != nil {
		return e
	}
	def, e := decode[governanceWorkflowExecution](root.Definition)
	if e != nil {
		return e
	}
	if def.Trial == nil || *def.Trial != trial {
		return conflict
	}
	_, version, e := governanceRead(tx, p, "workflow", def.Workflow.ID, def.Workflow.Version)
	if e != nil {
		return e
	}
	candidate, e := decode[GovernanceDefinition](version.Definition)
	if e != nil {
		return e
	}
	if candidate.Workflow == nil || fingerprint(encode(*candidate.Workflow)) != fingerprint(encode(def.Graph)) {
		return conflict
	}
	return governanceValidateTrial(tx, p, trial, def.Workflow, version.Definition, false)
}
func (s *Server) workflowTrialChild(tx *gorm.DB, p identity.Principal, parent, child string) (GovernanceRun, governanceFlowExecution, governanceFlowReport, []Field, error) {
	var run GovernanceRun
	var def governanceFlowExecution
	var report governanceFlowReport
	if e := tx.Where("id=? AND kind='dataflow' AND actor_id=? AND department_id=? AND workspace_id IS NOT NULL AND state='succeeded'", child, p.UserID, p.DepartmentID).Take(&run).Error; e != nil {
		return run, def, report, nil, e
	}
	def, e := decode[governanceFlowExecution](run.Definition)
	if e != nil {
		return run, def, report, nil, e
	}
	if def.WorkflowTrial == nil || def.WorkflowTrial.RunID != parent {
		return run, def, report, nil, denied
	}
	// Bound recursive authorization through predecessor artifacts.
	depth, _ := tx.Statement.Context.Value(governanceLineageDepthKey{}).(int)
	if depth >= 32 {
		return run, def, report, nil, problem(409, "GOVERNANCE_LINEAGE_DEPTH", "工作流试跑依赖链超过32层")
	}
	ctx := context.WithValue(governanceReadContext(tx.Statement.Context), governanceLineageDepthKey{}, depth+1)
	if e = s.governanceFlowAuthorize(ctx, p, def); e != nil {
		return run, def, report, nil, e
	}
	report, e = decode[governanceFlowReport](run.Result)
	if e != nil {
		return run, def, report, nil, e
	}
	_, plan, _, e := governanceExecutionPlan(def)
	if e != nil {
		return run, def, report, nil, e
	}
	fields := []Field{}
	for _, field := range plan.Outputs[GovernanceFlowEdge{Node: plan.OutputNode}] {
		fields = append(fields, field.Field)
	}
	var purged int64
	if e = tx.Table("governance_snapshot_purges").Where("run_id=?", run.ID).Count(&purged).Error; e != nil {
		return run, def, report, nil, e
	}
	if purged > 0 || report.Storage == nil {
		return run, def, report, nil, problem(410, "GOVERNANCE_SNAPSHOT_PURGED", "工作流试跑快照不可用，请重新试跑")
	}
	count, e := dorisResultCount(ctx, report.Storage, run.ID)
	if e == nil && count != report.OutputRows {
		e = conflict
	}
	return run, def, report, fields, e
}
func (s *Server) workflowTrialArtifactBasis(tx *gorm.DB, p identity.Principal, parent, child string, names []string) (governanceInput, error) {
	out := governanceInput{}
	run, def, report, fields, e := s.workflowTrialChild(tx, p, parent, child)
	if e != nil {
		return out, e
	}
	if !validFields(names, fields) {
		return out, invalid
	}
	selected := []Field{}
	for _, name := range names {
		for _, field := range fields {
			if field.Name == name {
				selected = append(selected, field)
			}
		}
	}
	out = governanceInput{Resource: Resource{Type: "governed_dataset", ID: child, Version: 1}, Fields: selected, DepartmentID: p.DepartmentID, Administrator: admin(p), WorkflowParent: parent, WorkflowTrialArtifact: true, ResourceFingerprint: fingerprint(run.Result), ProcessingFingerprint: fingerprint(run.Definition), Origins: map[string][]GovernanceFieldOrigin{}}
	for _, name := range names {
		out.Origins[name] = report.Lineage[name]
	}
	if def.WorkflowTrial == nil {
		return out, denied
	}
	return out, nil
}
func (s *Server) readWorkflowTrialArtifact(ctx context.Context, basis governanceInput, limit int, consume func([]interface{}) error) (int64, error) {
	tx := s.db.WithContext(ctx)
	var root GovernanceRun
	if e := tx.Where("id=?", basis.WorkflowParent).Take(&root).Error; e != nil {
		return 0, e
	}
	p, e := s.etlPrincipal(ctx, root.ActorID)
	if e != nil {
		return 0, e
	}
	run, _, report, _, e := s.workflowTrialChild(tx, p, basis.WorkflowParent, basis.Resource.ID)
	if e != nil {
		return 0, e
	}
	if fingerprint(run.Result) != basis.ResourceFingerprint || fingerprint(run.Definition) != basis.ProcessingFingerprint {
		return 0, conflict
	}
	count := min(limit, int(report.OutputRows))
	for start := 0; start < count; start += 250 {
		rows, e := dorisResultPage(ctx, report.Storage, run.ID, basis.Fields, start, min(250, count-start))
		if e != nil {
			return 0, e
		}
		for _, row := range rows {
			values := make([]interface{}, len(basis.Fields))
			for i, field := range basis.Fields {
				decoder := json.NewDecoder(bytes.NewReader(row[field.Name]))
				decoder.UseNumber()
				if e = decoder.Decode(&values[i]); e != nil {
					return 0, e
				}
			}
			if e = consume(values); e != nil {
				return 0, e
			}
		}
	}
	return report.OutputRows, nil
}
func (s *Server) workflowTrialEvidenceTask(tx *gorm.DB, p identity.Principal, parent GovernanceRun, task GovernanceWorkflowTask, child string) (GovernanceWorkflowTaskRun, error) {
	row := GovernanceWorkflowTaskRun{ParentRunID: parent.ID, TaskID: task.ID, State: "failed", ErrorCode: "WORKFLOW_EVIDENCE_UNAVAILABLE", Result: json.RawMessage("null")}
	_, _, report, _, e := s.workflowTrialChild(tx, p, parent.ID, child)
	if e != nil {
		var diagnostic *Problem
		if errors.As(e, &diagnostic) {
			row.ErrorCode = diagnostic.Code
			return row, nil
		}
		return row, e
	}
	total := report.OutputRows + report.RejectedRows
	row.Result = encode(gin.H{"trial": true, "publication_suppressed": governanceWorkflowTaskKind(task) == "publish_approval", "checked_rows": total, "rejected_rows": report.RejectedRows})
	if total > 0 && float64(report.RejectedRows)/float64(total) > task.MaxFailureRate {
		row.ErrorCode = "WORKFLOW_QUALITY_BLOCKED"
		return row, nil
	}
	row.State = "succeeded"
	row.ErrorCode = ""
	return row, nil
}
func (s *Server) workflowReleaseEvidence(tx *gorm.DB, p identity.Principal, body GovernanceRelease) error {
	if body.TrialRunID == "" {
		return problem(409, "GOVERNANCE_RELEASE_TRIAL_REQUIRED", "请先完成候选工作流完整试跑")
	}
	var root GovernanceRun
	if e := tx.Where("id=? AND workspace_id=? AND kind='workflow' AND state='succeeded'", body.TrialRunID, body.WorkspaceID).Take(&root).Error; e != nil {
		return problem(409, "GOVERNANCE_RELEASE_TRIAL_REQUIRED", "工作流完整试跑尚未成功")
	}
	def, e := decode[governanceWorkflowExecution](root.Definition)
	if e != nil {
		return e
	}
	if def.Trial == nil || def.Trial.Digest != body.CandidateDigest || def.Workflow != body.Candidate {
		return conflict
	}
	actor, e := s.etlPrincipal(tx.Statement.Context, root.ActorID)
	if e != nil {
		return e
	}
	if e = governanceWorkflowTrialValidate(tx, actor, *def.Trial); e != nil {
		return e
	}
	rows := []GovernanceWorkflowTaskRun{}
	if e = tx.Where("parent_run_id=?", root.ID).Find(&rows).Error; e != nil {
		return e
	}
	if len(rows) != len(def.Graph.Tasks) {
		return conflict
	}
	for _, row := range rows {
		if row.State == "skipped" {
			continue
		}
		if row.State != "succeeded" {
			return conflict
		}
		if row.ChildRunID != nil {
			_, _, report, _, e := s.workflowTrialChild(tx, actor, root.ID, *row.ChildRunID)
			if e != nil {
				return e
			}
			if report.RejectedRows > 0 {
				return problem(409, "GOVERNANCE_RELEASE_QUALITY", "工作流子任务仍有隔离记录，请整改后重新试跑")
			}
		}
	}
	return nil
}
