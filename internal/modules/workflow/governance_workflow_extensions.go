package workflow

import (
	"encoding/json"
	"errors"
	"slices"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func governanceWorkflowTaskKind(task GovernanceWorkflowTask) string {
	if task.Kind == "" {
		return "dataflow"
	}
	return task.Kind
}

func governanceWorkflowParameters(tx *gorm.DB, p identity.Principal, graph GovernanceWorkflow, values map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	declared := map[string]bool{}
	for _, task := range graph.Tasks {
		if governanceWorkflowTaskKind(task) != "dataflow" {
			continue
		}
		_, row, e := governanceRead(tx, p, "dataflow", task.Dataflow.ID, task.Dataflow.Version)
		if e != nil {
			return nil, e
		}
		if e = requireGovernanceFlowMode(row.ExecutionMode, "batch"); e != nil {
			return nil, e
		}
		def, e := decode[GovernanceDefinition](row.Definition)
		if e != nil {
			return nil, e
		}
		if def.Dataflow == nil {
			return nil, invalid
		}
		defaults, e := governanceFlowParameters(*def.Dataflow)
		if e != nil {
			return nil, e
		}
		for name := range defaults {
			declared[name] = true
		}
	}
	merged := map[string]json.RawMessage{}
	for name, value := range graph.Parameters {
		merged[name] = value
	}
	for name, value := range values {
		merged[name] = value
	}
	if len(merged) > 20 {
		return nil, invalid
	}
	for name, raw := range merged {
		if !declared[name] || !governanceIdentifier.MatchString(name) || len(raw) > 2048 {
			return nil, problem(400, "GOVERNANCE_PARAMETER_INVALID", "工作流参数没有对应的已声明任务参数")
		}
		var value interface{}
		if json.Unmarshal(raw, &value) != nil {
			return nil, invalid
		}
		switch value.(type) {
		case string, float64, bool:
		default:
			return nil, invalid
		}
	}
	return merged, nil
}

func governanceWorkflowTaskBindings(tx *gorm.DB, parent string, task GovernanceWorkflowTask, values map[string]json.RawMessage) (map[string]string, map[string]json.RawMessage, error) {
	bindings := map[string]string{}
	for node, dependency := range task.Inputs {
		var row GovernanceWorkflowTaskRun
		if e := tx.Where("parent_run_id=? AND task_id=? AND state='succeeded'", parent, dependency).Take(&row).Error; e != nil {
			return nil, nil, e
		}
		if row.ChildRunID == nil {
			return nil, nil, invalid
		}
		bindings[node] = *row.ChildRunID
	}
	var version GovernanceVersion
	if e := tx.Where("definition_id=? AND version=?", task.Dataflow.ID, task.Dataflow.Version).Take(&version).Error; e != nil {
		return nil, nil, e
	}
	def, e := decode[GovernanceDefinition](version.Definition)
	if e != nil {
		return nil, nil, e
	}
	if def.Dataflow == nil {
		return nil, nil, invalid
	}
	defaults, e := governanceFlowParameters(*def.Dataflow)
	if e != nil {
		return nil, nil, e
	}
	parameters := map[string]json.RawMessage{}
	for name, value := range values {
		if _, ok := defaults[name]; ok {
			parameters[name] = value
		}
	}
	return bindings, parameters, nil
}

func (s *Server) governanceWorkflowEvidenceTask(tx *gorm.DB, p identity.Principal, run GovernanceRun, task GovernanceWorkflowTask) (GovernanceWorkflowTaskRun, error) {
	row := GovernanceWorkflowTaskRun{ParentRunID: run.ID, TaskID: task.ID, State: "failed", ErrorCode: "WORKFLOW_EVIDENCE_UNAVAILABLE", Result: json.RawMessage("null")}
	var source GovernanceWorkflowTaskRun
	if e := tx.Where("parent_run_id=? AND task_id=? AND state='succeeded'", run.ID, task.InputTask).Take(&source).Error; e != nil {
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return row, nil
		}
		return row, e
	}
	if source.ChildRunID == nil {
		return row, nil
	}
	parentDef, e := decode[governanceWorkflowExecution](run.Definition)
	if e != nil {
		return row, e
	}
	if parentDef.Trial != nil {
		return s.workflowTrialEvidenceTask(tx, p, run, task, *source.ChildRunID)
	}
	dataset, e := get(tx, "dataset", *source.ChildRunID, false)
	if e != nil {
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return row, nil
		}
		return row, e
	}
	if dataset.OwnerID != run.ActorID {
		return row, denied
	}
	_, _, childRun, report, e := s.governanceOutputReady(tx.Statement.Context, tx, dataset)
	if e != nil {
		var diagnostic *Problem
		if errors.As(e, &diagnostic) {
			row.ErrorCode = diagnostic.Code
			return row, nil
		}
		return row, e
	}
	if childRun.WorkspaceID != run.WorkspaceID || childRun.DepartmentID != run.DepartmentID {
		return row, denied
	}
	row.Result = encode(gin.H{"dataset_id": dataset.ID, "contract_version": dataset.Version, "checked_rows": report.OutputRows + report.RejectedRows, "rejected_rows": report.RejectedRows})
	if governanceWorkflowTaskKind(task) == "quality_check" {
		total := report.OutputRows + report.RejectedRows
		if total > 0 && float64(report.RejectedRows)/float64(total) > task.MaxFailureRate {
			row.ErrorCode = "WORKFLOW_QUALITY_BLOCKED"
			return row, nil
		}
		row.State = "succeeded"
		row.ErrorCode = ""
		return row, nil
	}
	row.State = "waiting_approval"
	row.ErrorCode = "PUBLICATION_APPROVAL_REQUIRED"
	return row, nil
}

func (s *Server) registerGovernanceWorkflowExtensions(api, write *gin.RouterGroup) {
	api.GET("/governance/workflows/:id/versions/:version/parameters", s.require("governance", "governor"), func(c *gin.Context) {
		version, e := intPath(c, "version")
		if e != nil {
			s.fail(c, e)
			return
		}
		tx := s.db.WithContext(c.Request.Context())
		p := principal(c)
		_, row, e := governanceRead(tx, p, "workflow", c.Param("id"), version)
		if e != nil {
			s.fail(c, e)
			return
		}
		def, e := decode[GovernanceDefinition](row.Definition)
		if e != nil {
			s.fail(c, e)
			return
		}
		if def.Workflow == nil {
			s.fail(c, invalid)
			return
		}
		defaults := map[string]json.RawMessage{}
		for _, task := range def.Workflow.Tasks {
			if governanceWorkflowTaskKind(task) != "dataflow" {
				continue
			}
			_, child, e := governanceRead(tx, p, "dataflow", task.Dataflow.ID, task.Dataflow.Version)
			if e != nil {
				s.fail(c, e)
				return
			}
			flow, e := decode[GovernanceDefinition](child.Definition)
			if e != nil {
				s.fail(c, e)
				return
			}
			if flow.Dataflow == nil {
				s.fail(c, invalid)
				return
			}
			values, e := governanceFlowParameters(*flow.Dataflow)
			if e != nil {
				s.fail(c, e)
				return
			}
			for name, value := range values {
				if _, exists := defaults[name]; !exists {
					defaults[name] = value
				}
			}
		}
		for name, value := range def.Workflow.Parameters {
			defaults[name] = value
		}
		c.JSON(200, gin.H{"defaults": defaults})
	})
	write.POST("/governance/runs/:id/resume", s.require("governance", "governor"), s.mutatePath("governance_resume_workflow", s.resumeGovernanceWorkflow))
	api.GET("/governance/publication-approvals", s.require("assets", "provider"), func(c *gin.Context) {
		p := principal(c)
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
		q := s.db.WithContext(c.Request.Context()).Table("governance_workflow_tasks t").Joins("JOIN governance_runs r ON r.id=t.parent_run_id").Joins("JOIN business_records d ON d.kind='dataset' AND d.id=t.result->>'dataset_id'").Where("t.state='waiting_approval' AND r.state='running' AND r.cancel_requested=false")
		if !admin(p) {
			q = q.Where("d.department_id=? AND EXISTS(SELECT 1 FROM business_records a WHERE a.kind='assignment' AND a.state='active' AND a.department_id=d.department_id AND a.body->>'user_id'=? AND a.body->>'capability'='manage_resource' AND (a.body->>'resource_id'='*' OR a.body->>'resource_id'=d.id) AND (a.body->>'expires_at')::timestamptz>now())", p.DepartmentID, p.UserID)
		}
		var total int64
		if e = q.Count(&total).Error; e != nil {
			s.fail(c, e)
			return
		}
		rows := []struct {
			ParentRunID string          `json:"parent_run_id"`
			TaskID      string          `json:"task_id"`
			DatasetID   string          `json:"dataset_id"`
			Name        string          `json:"name"`
			ActorID     string          `json:"actor_id"`
			Result      json.RawMessage `json:"result"`
		}{}
		if e = q.Select("t.parent_run_id,t.task_id,d.id AS dataset_id,d.body->'definition'->>'name' AS name,r.actor_id,t.result").Order("r.created_at,t.task_id").Offset((page - 1) * size).Limit(size).Scan(&rows).Error; e != nil {
			s.fail(c, e)
			return
		}
		c.JSON(200, gin.H{"items": rows, "total": total, "page": page, "page_size": size})
	})
	for _, decision := range []string{"approve", "reject"} {
		decision := decision
		write.POST("/governance/publication-approvals/:id/:task/"+decision, s.require("assets", "provider"), func(c *gin.Context) {
			task := c.Param("task")
			s.mutatePath("governance_publication_"+decision, func(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
				input, e := bind[struct {
					DatasetID string `json:"dataset_id"`
					Note      string `json:"note"`
				}](b)
				if e != nil {
					return 0, nil, id, e
				}
				if !valid(input.Note, 2000) || !governanceIdentifier.MatchString(task) {
					return 0, nil, id, invalid
				}
				return s.governancePublicationDecision(tx, p, id, task, decision, input.DatasetID, input.Note)
			})(c)
		})
	}
}

func (s *Server) governancePublicationDecision(tx *gorm.DB, p identity.Principal, id, task, decision, datasetID, note string) (int, json.RawMessage, string, error) {
	var run GovernanceRun
	if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND kind='workflow'", id).Take(&run).Error; e != nil {
		return 0, nil, id, e
	}
	if e := governanceWorkspaceMatch(tx.Statement.Context, run.WorkspaceID); e != nil {
		return 0, nil, id, e
	}
	if run.State != "running" || run.CancelRequested {
		return 0, nil, id, conflict
	}
	if !admin(p) && run.ActorID == p.UserID {
		return 0, nil, id, problem(403, "PUBLICATION_SELF_APPROVAL", "请由另一位有发布职责的人员核验")
	}
	var row GovernanceWorkflowTaskRun
	if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("parent_run_id=? AND task_id=?", id, task).Take(&row).Error; e != nil {
		return 0, nil, id, e
	}
	if row.State != "waiting_approval" {
		return 0, nil, id, conflict
	}
	result, e := decode[map[string]interface{}](row.Result)
	if e != nil {
		return 0, nil, id, e
	}
	if result["dataset_id"] != datasetID {
		return 0, nil, id, conflict
	}
	dataset, e := get(tx, "dataset", datasetID, true)
	if e != nil {
		return 0, nil, id, e
	}
	if !admin(p) && dataset.DepartmentID != p.DepartmentID {
		return 0, nil, id, denied
	}
	if e = requireResourceCapability(tx, p, Resource{Type: "dataset", ID: datasetID}, "manage_resource"); e != nil {
		return 0, nil, id, e
	}
	if e = governanceDatasetWorkspaceWrite(tx, p, datasetID); e != nil {
		return 0, nil, id, e
	}
	row.State = "failed"
	row.ErrorCode = "PUBLICATION_REJECTED"
	if decision == "approve" {
		if dataset.State != "published" {
			if _, _, _, e = s.publishDataset(tx, p, datasetID, []byte(`{}`)); e != nil {
				return 0, nil, id, e
			}
		} else {
			if _, _, _, _, e = s.governanceOutputReady(tx.Statement.Context, tx, dataset); e != nil {
				return 0, nil, id, e
			}
			if e = requireDatasetProcessingBasis(tx, dataset, p); e != nil {
				return 0, nil, id, e
			}
		}
		row.State = "succeeded"
		row.ErrorCode = ""
	}
	result["decision"] = decision
	result["approved_by"] = p.UserID
	result["note"] = note
	result["decided_at"] = time.Now().UTC()
	row.Result = encode(result)
	if e = tx.Save(&row).Error; e != nil {
		return 0, nil, id, e
	}
	if e = tx.Create(&GovernanceRunEvent{RunID: run.ID, NodeID: task, Code: "PUBLICATION_" + decision}).Error; e != nil {
		return 0, nil, id, e
	}
	return 200, encode(row), id, nil
}

func (s *Server) resumeGovernanceWorkflow(tx *gorm.DB, p identity.Principal, id string, b []byte) (int, json.RawMessage, string, error) {
	if _, e := bind[struct{}](b); e != nil {
		return 0, nil, id, e
	}
	old, e := governanceRunRead(tx, p, id, true)
	if e != nil {
		return 0, nil, id, e
	}
	if old.Kind != "workflow" || !slices.Contains([]string{"failed", "cancelled", "interrupted"}, old.State) {
		return 0, nil, id, conflict
	}
	var active int64
	if e = tx.Table("governance_workflow_tasks t").Joins("JOIN governance_runs r ON r.id=t.child_run_id").Where("t.parent_run_id=? AND (r.state IN ('queued','running') OR r.error_code='GOVERNANCE_COMMIT_UNKNOWN')", id).Count(&active).Error; e != nil {
		return 0, nil, id, e
	}
	if active > 0 {
		return 0, nil, id, problem(409, "WORKFLOW_RECOVERY_BLOCKED", "请先核对仍在运行或提交结果不确定的子任务")
	}
	execution, e := decode[governanceWorkflowExecution](old.Definition)
	if e != nil {
		return 0, nil, id, e
	}
	status, body, nextID, e := s.enqueueGovernanceWorkflowParameters(workspaceContext(tx, old.WorkspaceID, true), p, execution.Workflow, &old.ID, execution.Parameters)
	if e != nil {
		return 0, nil, id, e
	}
	rows := []GovernanceWorkflowTaskRun{}
	if e = tx.Where("parent_run_id=?", id).Find(&rows).Error; e != nil {
		return 0, nil, id, e
	}
	byID := map[string]GovernanceWorkflowTaskRun{}
	for _, row := range rows {
		byID[row.TaskID] = row
	}
	reused := map[string]bool{}
	for pass := 0; pass < len(execution.Graph.Tasks); pass++ {
		for _, task := range execution.Graph.Tasks {
			previous := byID[task.ID]
			if reused[task.ID] || previous.State != "succeeded" || task.Condition == "all_failure" || task.Condition == "all_done" || slices.ContainsFunc(task.DependsOn, func(dep string) bool { return !reused[dep] }) {
				continue
			}
			if previous.ChildRunID != nil {
				dataset, e := get(tx, "dataset", *previous.ChildRunID, false)
				if e != nil {
					return 0, nil, id, e
				}
				if dataset.OwnerID != p.UserID || slices.Contains([]string{"withdrawn", "disabled", "archived"}, dataset.State) {
					continue
				}
				if _, _, _, _, e = s.governanceOutputReady(tx.Statement.Context, tx, dataset); e != nil {
					var diagnostic *Problem
					if errors.As(e, &diagnostic) {
						continue
					}
					return 0, nil, id, e
				}
			}
			if governanceWorkflowTaskKind(task) == "publish_approval" {
				result, e := decode[map[string]interface{}](previous.Result)
				if e != nil {
					return 0, nil, id, e
				}
				datasetID, ok := result["dataset_id"].(string)
				if !ok {
					continue
				}
				dataset, e := get(tx, "dataset", datasetID, false)
				if e != nil {
					return 0, nil, id, e
				}
				if dataset.State != "published" {
					continue
				}
			}
			previous.ParentRunID = nextID
			previous.ReusedFromRun = &old.ID
			if e = tx.Save(&previous).Error; e != nil {
				return 0, nil, id, e
			}
			reused[task.ID] = true
		}
	}
	var next GovernanceRun
	if e = json.Unmarshal(body, &next); e != nil {
		return 0, nil, id, e
	}
	if e = tx.Create(&GovernanceRunEvent{RunID: nextID, Code: "WORKFLOW_RESUMED", Processed: int64(len(reused))}).Error; e != nil {
		return 0, nil, id, e
	}
	return status, encode(next), nextID, nil
}
