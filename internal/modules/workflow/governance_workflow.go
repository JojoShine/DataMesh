package workflow

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"sync"
	"time"

	"datamesh/internal/modules/identity"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Task dependencies and values are declarative; no executable expressions enter a workflow.
type GovernanceWorkflow struct {
	Parameters map[string]json.RawMessage `json:"parameters,omitempty"`
	Tasks      []GovernanceWorkflowTask   `json:"tasks"`
}
type GovernanceWorkflowTask struct {
	Kind           string            `json:"kind,omitempty"`
	InputTask      string            `json:"input_task,omitempty"`
	Inputs         map[string]string `json:"inputs,omitempty"`
	MaxFailureRate float64           `json:"max_failure_rate,omitempty"`
	ID             string            `json:"id"`
	Dataflow       GovernanceRef     `json:"dataflow"`
	DependsOn      []string          `json:"depends_on"`
	Condition      string            `json:"condition"`
}
type governanceWorkflowExecution struct {
	Trial      *governanceTrial           `json:"trial,omitempty"`
	Parameters map[string]json.RawMessage `json:"parameters,omitempty"`
	Workflow   GovernanceRef              `json:"workflow"`
	Graph      GovernanceWorkflow         `json:"graph"`
}
type GovernanceWorkflowTaskRun struct {
	Result        json.RawMessage `json:"result" gorm:"type:jsonb"`
	ReusedFromRun *string         `json:"reused_from_run,omitempty"`
	ParentRunID   string          `json:"parent_run_id" gorm:"primaryKey"`
	TaskID        string          `json:"task_id" gorm:"primaryKey"`
	ChildRunID    *string         `json:"child_run_id"`
	State         string          `json:"state"`
	ErrorCode     string          `json:"error_code"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

func (GovernanceWorkflowTaskRun) TableName() string { return "governance_workflow_tasks" }

func validateGovernanceWorkflow(graph GovernanceWorkflow) error {
	if len(graph.Parameters) > 20 {
		return invalid
	}
	for name, value := range graph.Parameters {
		if !governanceIdentifier.MatchString(name) || len(value) > 2048 {
			return invalid
		}
	}
	if len(graph.Tasks) < 1 || len(graph.Tasks) > 40 {
		return invalid
	}
	nodes := map[string]GovernanceWorkflowTask{}
	for _, task := range graph.Tasks {
		if !governanceIdentifier.MatchString(task.ID) || nodes[task.ID].ID != "" || len(task.DependsOn) > 40 {
			return invalid
		}
		kind := governanceWorkflowTaskKind(task)
		if kind != "dataflow" && kind != "quality_check" && kind != "publish_approval" {
			return invalid
		}
		if kind == "dataflow" {
			if !validGovernanceRef(&task.Dataflow) || task.InputTask != "" || task.MaxFailureRate != 0 || len(task.Inputs) > 4 {
				return invalid
			}
		} else {
			if task.Dataflow.ID != "" || task.Dataflow.Version != 0 || !governanceIdentifier.MatchString(task.InputTask) || len(task.Inputs) > 0 || task.MaxFailureRate < 0 || task.MaxFailureRate > 1 || kind == "publish_approval" && task.MaxFailureRate != 0 {
				return invalid
			}
		}
		if task.Condition != "" && task.Condition != "all_success" && task.Condition != "all_failure" && task.Condition != "all_done" {
			return invalid
		}
		nodes[task.ID] = task
	}
	visited := map[string]int{}
	var visit func(string) error
	visit = func(id string) error {
		if visited[id] == 1 {
			return problem(400, "WORKFLOW_CYCLE", "工作流依赖包含环")
		}
		if visited[id] == 2 {
			return nil
		}
		task, ok := nodes[id]
		if !ok {
			return invalid
		}
		visited[id] = 1
		seen := map[string]bool{}
		for _, dep := range task.DependsOn {
			if seen[dep] {
				return invalid
			}
			seen[dep] = true
			if err := visit(dep); err != nil {
				return err
			}
		}
		visited[id] = 2
		return nil
	}
	for _, task := range graph.Tasks {
		for node, source := range task.Inputs {
			if !governanceIdentifier.MatchString(node) || !slices.Contains(task.DependsOn, source) || governanceWorkflowTaskKind(nodes[source]) != "dataflow" || nodes[source].ID == "" {
				return invalid
			}
		}
		if task.InputTask != "" && (!slices.Contains(task.DependsOn, task.InputTask) || nodes[task.InputTask].ID == "" || governanceWorkflowTaskKind(nodes[task.InputTask]) != "dataflow") {
			return invalid
		}
		if err := visit(task.ID); err != nil {
			return err
		}
	}
	return nil
}
func governanceWorkflowTerminal(state string) bool {
	return state == "succeeded" || state == "failed" || state == "cancelled" || state == "interrupted" || state == "skipped"
}
func governanceWorkflowReady(task GovernanceWorkflowTask, states map[string]string) (bool, bool) {
	if len(task.DependsOn) == 0 {
		return true, false
	}
	match := true
	for _, dep := range task.DependsOn {
		state := states[dep]
		if !governanceWorkflowTerminal(state) {
			return false, false
		}
		switch task.Condition {
		case "all_done":
		case "all_failure":
			match = match && (state == "failed" || state == "interrupted")
		default:
			match = match && state == "succeeded"
		}
	}
	return match, !match
}
func (s *Server) registerGovernanceWorkflows(api, write *gin.RouterGroup) {
	write.POST("/governance/workflows/:id/versions/:version/run", s.require("governance", "governor"), func(c *gin.Context) {
		version, e := intPath(c, "version")
		if e != nil {
			s.fail(c, e)
			return
		}
		ref := GovernanceRef{ID: c.Param("id"), Version: version}
		s.mutate("governance_run_workflow", func(tx *gorm.DB, p identity.Principal, b []byte) (int, json.RawMessage, string, error) {
			values, e := bind[governanceFlowRunInput](b)
			if e != nil {
				return 0, nil, ref.ID, e
			}
			return s.enqueueGovernanceWorkflowParameters(tx, p, ref, nil, values.Parameters)
		})(c)
	})
	api.GET("/governance/runs/:id/tasks", s.require("governance", "governor"), func(c *gin.Context) {
		tx := s.db.WithContext(c.Request.Context())
		run, e := governanceRunRead(tx, principal(c), c.Param("id"), false)
		if e != nil {
			s.fail(c, e)
			return
		}
		if run.Kind != "workflow" {
			s.fail(c, invalid)
			return
		}
		rows := []GovernanceWorkflowTaskRun{}
		if e = tx.Where("parent_run_id=?", run.ID).Order("task_id").Find(&rows).Error; e != nil {
			s.fail(c, e)
			return
		}
		c.JSON(200, gin.H{"items": rows})
	})
	s.registerGovernanceSchedules(api, write)
	s.registerGovernanceWorkflowExtensions(api, write)
}
func (s *Server) enqueueGovernanceWorkflow(tx *gorm.DB, p identity.Principal, ref GovernanceRef, retry *string) (int, json.RawMessage, string, error) {
	return s.enqueueGovernanceWorkflowParameters(tx, p, ref, retry, nil)
}
func (s *Server) enqueueGovernanceWorkflowParameters(tx *gorm.DB, p identity.Principal, ref GovernanceRef, retry *string, parameters map[string]json.RawMessage) (int, json.RawMessage, string, error) {
	head, row, e := governanceRead(tx, p, "workflow", ref.ID, ref.Version)
	if e != nil {
		return 0, nil, ref.ID, e
	}
	space, scopeErr := governanceWorkspaceWrite(tx, p, workspaceID(head.WorkspaceID))
	if scopeErr != nil {
		return 0, nil, ref.ID, scopeErr
	}
	var trial *governanceTrial
	if request, ok := tx.Statement.Context.Value(governanceTrialContextKey{}).(governanceTrial); ok {
		if row.State != "draft" || head.LatestVersion != ref.Version {
			return 0, nil, ref.ID, conflict
		}
		if e = governanceValidateTrial(tx, p, request, ref, row.Definition, true); e != nil {
			return 0, nil, ref.ID, e
		}
		trial = &request
	} else if row.State != "published" {
		return 0, nil, ref.ID, conflict
	}
	def, e := decode[GovernanceDefinition](row.Definition)
	if e != nil {
		return 0, nil, ref.ID, e
	}
	if def.Workflow == nil {
		return 0, nil, ref.ID, invalid
	}
	if e = validateGovernanceWorkflow(*def.Workflow); e != nil {
		return 0, nil, ref.ID, e
	}
	if e = governanceValidateReferences(tx, p, head.DepartmentID, workspaceID(head.WorkspaceID), def); e != nil {
		return 0, nil, ref.ID, e
	}
	values, e := governanceWorkflowParameters(tx, p, *def.Workflow, parameters)
	if e != nil {
		return 0, nil, ref.ID, e
	}
	for _, task := range def.Workflow.Tasks {
		if governanceWorkflowTaskKind(task) != "dataflow" {
			continue
		}
		_, v, err := governanceRead(tx, p, "dataflow", task.Dataflow.ID, task.Dataflow.Version)
		if err != nil {
			return 0, nil, ref.ID, err
		}
		if v.State != "published" {
			return 0, nil, ref.ID, conflict
		}
		flow, err := decode[GovernanceDefinition](v.Definition)
		if err != nil {
			return 0, nil, ref.ID, err
		}
		if flow.Dataflow == nil {
			return 0, nil, ref.ID, invalid
		}
		defaults, err := governanceFlowParameters(*flow.Dataflow)
		if err != nil {
			return 0, nil, ref.ID, err
		}
		childValues := map[string]json.RawMessage{}
		for name, value := range values {
			if _, ok := defaults[name]; ok {
				childValues[name] = value
			}
		}
		effective, err := governanceMaterializeFlow(*flow.Dataflow, childValues, nil)
		if err != nil {
			return 0, nil, ref.ID, err
		}
		for _, n := range effective.Nodes {
			if n.Source != nil && task.Inputs[n.ID] == "" {
				if _, err = s.governanceInputBasisParameters(tx, p, n.Source.Resource, n.Source.Fields, true, n.Source.Parameters); err != nil {
					return 0, nil, ref.ID, err
				}
			}
		}
	}
	if e = tx.Exec("SELECT pg_advisory_xact_lock(619483219)").Error; e != nil {
		return 0, nil, ref.ID, e
	}
	var active int64
	if e = tx.Model(&GovernanceRun{}).Where("actor_id=? AND kind='workflow' AND state IN ('queued','running')", p.UserID).Count(&active).Error; e != nil {
		return 0, nil, ref.ID, e
	}
	if active >= 10 {
		return 0, nil, ref.ID, conflict
	}
	id, e := newID()
	if e != nil {
		return 0, nil, ref.ID, e
	}
	if trial != nil {
		trial.RunID = id
	}
	run := GovernanceRun{ID: id, WorkspaceID: space.ID, Kind: "workflow", DepartmentID: head.DepartmentID, ActorID: p.UserID, Definition: encode(governanceWorkflowExecution{Trial: trial, Workflow: ref, Graph: *def.Workflow, Parameters: values}), State: "queued", RetryOf: retry, Result: json.RawMessage("null"), LeaseExpiresAt: time.Unix(0, 0).UTC()}
	if e = tx.Create(&run).Error; e != nil {
		return 0, nil, id, e
	}
	for _, task := range def.Workflow.Tasks {
		if e = tx.Create(&GovernanceWorkflowTaskRun{ParentRunID: id, TaskID: task.ID, State: "pending", Result: json.RawMessage("null")}).Error; e != nil {
			return 0, nil, id, e
		}
	}
	if e = governanceRunEvent(tx, run, "WORKFLOW_QUEUED"); e != nil {
		return 0, nil, id, e
	}
	return 201, encode(run), id, nil
}
func (s *Server) retryGovernanceWorkflow(tx *gorm.DB, p identity.Principal, run GovernanceRun) (int, json.RawMessage, string, error) {
	if run.State != "failed" && run.State != "interrupted" && run.State != "cancelled" {
		return 0, nil, run.ID, conflict
	}
	var uncertain int64
	if e := tx.Table("governance_workflow_tasks t").Joins("JOIN governance_runs r ON r.id=t.child_run_id").Where("t.parent_run_id=? AND (r.state IN ('queued','running') OR r.error_code='GOVERNANCE_COMMIT_UNKNOWN')", run.ID).Count(&uncertain).Error; e != nil {
		return 0, nil, run.ID, e
	}
	if uncertain > 0 {
		return 0, nil, run.ID, problem(409, "WORKFLOW_RECOVERY_BLOCKED", "子任务仍在运行或提交结果不确定，请先核对")
	}
	execution, e := decode[governanceWorkflowExecution](run.Definition)
	if e != nil {
		return 0, nil, run.ID, e
	}
	if execution.Trial != nil {
		return 0, nil, run.ID, problem(409, "GOVERNANCE_RELEASE_TRIAL_REQUIRED", "请从变更申请重新启动完整工作流试跑")
	}
	// Recovery is an explicit new complete run with the original fixed references.
	return s.enqueueGovernanceWorkflowParameters(tx, p, execution.Workflow, &run.ID, execution.Parameters)
}

// Orchestration only dispatches/polls durable children; it never occupies a
// data worker while waiting, so two parallel workflows cannot exhaust workers.
func (s *Server) StartGovernanceOrchestrator(parent context.Context) func() {
	ctx, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			tickCtx, tickCancel := context.WithTimeout(ctx, 15*time.Second)
			e := s.governanceOrchestratorTick(tickCtx)
			tickCancel()
			if e != nil && ctx.Err() == nil {
				slog.Error("governance orchestration unavailable")
			}
		}
	}()
	return func() { cancel(); wg.Wait() }
}
func (s *Server) governanceOrchestratorTick(ctx context.Context) error {
	// One lock orders schedule and workflow mutations across all application replicas.
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Exec("SELECT pg_advisory_xact_lock(619483220)").Error; e != nil {
			return e
		}
		if e := s.governanceScheduleTick(tx, time.Now().UTC()); e != nil {
			return e
		}
		runs := []GovernanceRun{}
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("kind='workflow' AND (state IN ('queued','running') OR (cancel_requested AND EXISTS (SELECT 1 FROM governance_workflow_tasks t JOIN governance_runs c ON c.id=t.child_run_id WHERE t.parent_run_id=governance_runs.id AND c.state IN ('queued','running'))))").Order("created_at,id").Limit(50).Find(&runs).Error; e != nil {
			return e
		}
		for _, run := range runs {
			if e := s.advanceGovernanceWorkflow(tx, run); e != nil {
				return e
			}
		}
		return nil
	})
}
func (s *Server) advanceGovernanceWorkflow(tx *gorm.DB, run GovernanceRun) error {
	execution, e := decode[governanceWorkflowExecution](run.Definition)
	if e != nil {
		return e
	}
	now := time.Now().UTC()
	if run.State == "queued" {
		run.State = "running"
		run.StartedAt = &now
		if e = tx.Save(&run).Error; e != nil {
			return e
		}
		if e = governanceRunEvent(tx, run, "WORKFLOW_STARTED"); e != nil {
			return e
		}
	}
	rows := []GovernanceWorkflowTaskRun{}
	if e = tx.Where("parent_run_id=?", run.ID).Find(&rows).Error; e != nil {
		return e
	}
	states := map[string]string{}
	for i := range rows {
		if rows[i].ChildRunID != nil {
			var child GovernanceRun
			if e = tx.Where("id=?", *rows[i].ChildRunID).Take(&child).Error; e != nil {
				return e
			}
			rows[i].State = child.State
			rows[i].ErrorCode = child.ErrorCode
			if run.CancelRequested && (child.State == "queued" || child.State == "running") {
				child.CancelRequested = true
				if child.State == "queued" {
					child.State = "cancelled"
					child.FinishedAt = &now
					child.ErrorCode = "GOVERNANCE_CANCELLED"
				}
				if e = tx.Save(&child).Error; e != nil {
					return e
				}
				rows[i].State = child.State
			}
		}
		if run.CancelRequested && (rows[i].State == "pending" || rows[i].State == "waiting_approval") {
			rows[i].State = "cancelled"
		}
		if e = tx.Save(&rows[i]).Error; e != nil {
			return e
		}
		states[rows[i].TaskID] = rows[i].State
	}
	p, authErr := s.etlPrincipal(tx.Statement.Context, run.ActorID)
	if authErr == nil && execution.Trial != nil {
		authErr = governanceWorkflowTrialValidate(tx, p, *execution.Trial)
	}
	for _, task := range execution.Graph.Tasks {
		if states[task.ID] != "pending" || run.CancelRequested {
			continue
		}
		ready, skip := governanceWorkflowReady(task, states)
		if !ready && !skip {
			continue
		}
		row := GovernanceWorkflowTaskRun{ParentRunID: run.ID, TaskID: task.ID, State: "skipped", Result: json.RawMessage("null")}
		if ready {
			var pending int64
			if e = tx.Model(&GovernanceRun{}).Where("actor_id=? AND kind<>'workflow' AND state IN ('queued','running')", run.ActorID).Count(&pending).Error; e != nil {
				return e
			}
			if pending >= 10 && governanceWorkflowTaskKind(task) == "dataflow" {
				continue
			}
			if authErr != nil {
				row.State = "failed"
				row.ErrorCode = "FORBIDDEN"
			} else if governanceWorkflowTaskKind(task) != "dataflow" {
				row, e = s.governanceWorkflowEvidenceTask(tx, p, run, task)
				if e != nil {
					return e
				}
			} else {
				// A savepoint ensures a rejected child leaves no half-created run or event.
				var body json.RawMessage
				var childID string
				err := tx.Transaction(func(childTx *gorm.DB) error {
					bindings, values, e := governanceWorkflowTaskBindings(childTx, run.ID, task, execution.Parameters)
					if e != nil {
						return e
					}
					childScope := workspaceContext(childTx, run.WorkspaceID, true)
					if execution.Trial != nil {
						childScope = childScope.WithContext(context.WithValue(childScope.Statement.Context, governanceWorkflowTrialContextKey{}, *execution.Trial))
					}
					_, result, id, err := s.enqueueGovernanceFlowBindings(childScope, p, task.Dataflow, encode(governanceFlowRunInput{Parameters: values}), bindings, run.ID)
					body = result
					childID = id
					return err
				})
				if err != nil {
					row.State = "failed"
					row.ErrorCode = "WORKFLOW_DISPATCH_DENIED"
				} else {
					var child GovernanceRun
					if e = json.Unmarshal(body, &child); e != nil {
						return e
					}
					row.State = child.State
					row.ChildRunID = &childID
				}
			}
		}
		if e = tx.Save(&row).Error; e != nil {
			return e
		}
		states[task.ID] = row.State
		if e = tx.Create(&GovernanceRunEvent{RunID: run.ID, NodeID: task.ID, Code: "WORKFLOW_TASK_" + row.State}).Error; e != nil {
			return e
		}
	}
	complete, failed := true, false
	for _, state := range states {
		complete = complete && governanceWorkflowTerminal(state)
		failed = failed || state == "failed" || state == "interrupted" || state == "cancelled"
	}
	if !complete {
		return nil
	}
	if run.State == "succeeded" || run.State == "failed" || run.State == "cancelled" {
		return nil
	}
	run.State = "succeeded"
	if failed {
		run.State = "failed"
		run.ErrorCode = "WORKFLOW_TASK_FAILED"
	}
	if run.CancelRequested {
		run.State = "cancelled"
		run.ErrorCode = "GOVERNANCE_CANCELLED"
	}
	run.FinishedAt = &now
	run.Result = encode(states)
	if e = tx.Save(&run).Error; e != nil {
		return e
	}
	if e = governanceRunEvent(tx, run, "WORKFLOW_"+run.State); e != nil {
		return e
	}
	return s.audit(tx, p, "governance_workflow_finished", run.ID, run.State, "")
}
