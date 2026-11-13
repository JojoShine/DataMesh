package workflow

import (
	"datamesh/internal/modules/identity"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"reflect"
	"slices"
	"time"
)

type governanceIssueEvidence struct {
	Profile       governanceProfileDefinition `json:"profile"`
	PolicyVersion int                         `json:"policy_version,omitempty"`
	Policy        json.RawMessage             `json:"policy,omitempty"`
	BaselineID    *string                     `json:"baseline_id,omitempty"`
}
type governanceIssueContext struct {
	Eligible        bool   `json:"eligible"`
	OriginKind      string `json:"origin_kind"`
	Summary         string `json:"summary"`
	ExistingIssueID string `json:"existing_issue_id"`
	DepartmentID    string `json:"department_id"`
}

func (s *Server) profileIssueEvidence(tx *gorm.DB, p identity.Principal, run GovernanceRun) (GovernanceQualityIssue, governanceIssueContext, error) {
	issue := GovernanceQualityIssue{RunID: run.ID, DepartmentID: run.DepartmentID}
	info := governanceIssueContext{DepartmentID: run.DepartmentID}
	if run.Kind != "profile" || run.State != "succeeded" {
		return issue, info, problem(409, "QUALITY_ISSUE_NOT_REQUIRED", "此运行没有可登记的完整核验异常")
	}
	def, e := decode[governanceProfileDefinition](run.Definition)
	if e != nil {
		return issue, info, e
	}
	report, e := decode[ProfileReport](run.Result)
	if e != nil {
		return issue, info, e
	}
	if def.Mode != "full" || report.Coverage != "full" || report.Rows != report.TotalRows {
		return issue, info, problem(409, "QUALITY_FULL_EVIDENCE_REQUIRED", "完整核验异常才能进入统一整改")
	}
	if e = s.governanceCheckInput(governanceReadContext(tx.Statement.Context), p, def.Input, false); e != nil {
		return issue, info, e
	}
	evidence := governanceIssueEvidence{Profile: def}
	var observation GovernanceMonitorObservation
	e = tx.Where("run_id=?", run.ID).Take(&observation).Error
	if e == nil {
		monitor, e := monitorAccess(tx, p, observation.MonitorID, false)
		if e != nil {
			return issue, info, e
		}
		if observation.State != "alert" || observation.ResolutionState == "closed" {
			return issue, info, problem(409, "QUALITY_ISSUE_NOT_REQUIRED", "此监测没有尚待处理的异常")
		}
		if observation.PolicyVersion != monitor.Version || fingerprint(observation.Policy) != fingerprint(monitor.Policy) {
			return issue, info, problem(409, "MONITOR_ISSUE_CONFIGURATION_CHANGED", "原异常的监测配置已变化，请重新监测后登记")
		}
		evidence.PolicyVersion = observation.PolicyVersion
		evidence.Policy = observation.Policy
		evidence.BaselineID = monitor.BaselineID
		issue.OriginKind = "monitor"
		issue.MonitorID = &monitor.ID
		issue.ObservationID = &observation.ID
		info.OriginKind = "monitor"
		info.Summary = "持续监测发现阈值异常，按固定配置与基线复测"
	} else if !errors.Is(e, gorm.ErrRecordNotFound) {
		return issue, info, e
	} else {
		if report.StandardAudit == nil || report.StandardAudit.RejectedRows == 0 || len(def.StandardBindings) == 0 {
			return issue, info, problem(409, "QUALITY_ISSUE_NOT_REQUIRED", "此运行没有已核实的标准违规")
		}
		issue.OriginKind = "standard"
		info.OriginKind = "standard"
		info.Summary = "完整标准核验存在违规记录，复测固定原来源和标准版本"
	}
	issue.Evidence = encode(evidence)
	info.Eligible = true
	return issue, info, nil
}
func (s *Server) registerGovernanceIssueEvidence(api *gin.RouterGroup) {
	api.GET("/governance/runs/:id/issue-context", s.require("governance", "governor"), func(c *gin.Context) {
		tx := s.db.WithContext(c.Request.Context())
		p := principal(c)
		run, e := governanceRunRead(tx, p, c.Param("id"), false)
		if e != nil {
			s.fail(c, e)
			return
		}
		var info governanceIssueContext
		if run.Kind == "profile" {
			_, info, e = s.profileIssueEvidence(tx, p, run)
			if e != nil {
				var problemValue *Problem
				if !errors.As(e, &problemValue) || problemValue.Code != "QUALITY_ISSUE_NOT_REQUIRED" {
					s.fail(c, e)
					return
				}
			}
		} else {
			info.DepartmentID = run.DepartmentID
			info.OriginKind = "dataflow"
		}
		var existing GovernanceQualityIssue
		if e = tx.Where("run_id=?", run.ID).Limit(1).Find(&existing).Error; e != nil {
			s.fail(c, e)
			return
		}
		if existing.ID != "" {
			info.ExistingIssueID = existing.ID
			info.Eligible = false
		}
		c.JSON(200, info)
	})
}
func (s *Server) createProfileIssue(tx *gorm.DB, p identity.Principal, run GovernanceRun, assignee string, due time.Time) (int, json.RawMessage, string, error) {
	issue, _, e := s.profileIssueEvidence(tx, p, run)
	if e != nil {
		return 0, nil, run.ID, e
	}
	if e = governanceMember(tx, assignee, run.DepartmentID); e != nil {
		return 0, nil, run.ID, e
	}
	if !due.After(time.Now()) || due.After(time.Now().AddDate(1, 0, 0)) {
		return 0, nil, run.ID, invalid
	}
	if e = governanceLock(tx, run.DepartmentID); e != nil {
		return 0, nil, run.ID, e
	}
	var count int64
	if e = tx.Model(&GovernanceQualityIssue{}).Where("run_id=?", run.ID).Count(&count).Error; e != nil {
		return 0, nil, run.ID, e
	}
	if count > 0 {
		return 0, nil, run.ID, conflict
	}
	issue.ID, e = newID()
	if e != nil {
		return 0, nil, run.ID, e
	}
	issue.ActorID = p.UserID
	issue.AssigneeID = assignee
	issue.DueAt = due.UTC()
	issue.State = "open"
	issue.Version = 1
	if e = tx.Omit("FlowID", "FlowVersion").Create(&issue).Error; e != nil {
		return 0, nil, issue.ID, e
	}
	if e = tx.Create(&GovernanceQualityIssueEvent{IssueID: issue.ID, ActorID: p.UserID, Action: "created", RunID: &run.ID}).Error; e != nil {
		return 0, nil, issue.ID, e
	}
	if e = governanceIssueNotice(tx, issue, "assigned"); e != nil {
		return 0, nil, issue.ID, e
	}
	return 201, encode(issue), issue.ID, nil
}
func (s *Server) retestProfileIssue(tx *gorm.DB, p identity.Principal, issue GovernanceQualityIssue) (string, error) {
	evidence, e := decode[governanceIssueEvidence](issue.Evidence)
	if e != nil {
		return "", e
	}
	basis, e := s.governanceInputBasisParameters(tx, p, evidence.Profile.Input.Resource, fieldNames(evidence.Profile.Input.Fields), false, evidence.Profile.Input.Parameters)
	if e != nil {
		return "", e
	}
	if !governanceIssueInputMatches(basis, evidence.Profile.Input) {
		return "", problem(409, "QUALITY_RETEST_EVIDENCE", "原来源契约或连接配置已变化，请重新核验当前来源")
	}
	if issue.OriginKind == "monitor" {
		if issue.MonitorID == nil || issue.ObservationID == nil {
			return "", invalid
		}
		monitor, e := monitorAccess(tx, p, *issue.MonitorID, true)
		if e != nil {
			return "", e
		}
		if monitor.Version != evidence.PolicyVersion || fingerprint(monitor.Policy) != fingerprint(evidence.Policy) || !reflect.DeepEqual(monitor.BaselineID, evidence.BaselineID) {
			return "", problem(409, "MONITOR_ISSUE_CONFIGURATION_CHANGED", "监测配置或基线已变化，不能用于此工单复测；请复核原问题并登记当前异常")
		}
		out, e := s.monitorQueue(tx, p, &monitor)
		if e != nil {
			return "", e
		}
		return *out.RunID, nil
	}
	if _, _, e = resolveStandardBindings(tx, p, evidence.Profile.StandardBindings); e != nil {
		return "", e
	}
	def := evidence.Profile
	_, raw, id, e := s.enqueueGovernanceProfile(tx, p, governanceProfileInput{Resource: def.Input.Resource, Fields: fieldNames(def.Input.Fields), Mode: "full", Parameters: def.Input.Parameters}, nil)
	if e != nil {
		return "", e
	}
	var run GovernanceRun
	if e = json.Unmarshal(raw, &run); e != nil {
		return "", e
	}
	next, e := decode[governanceProfileDefinition](run.Definition)
	if e != nil {
		return "", e
	}
	next.StandardBindings = def.StandardBindings
	if e = tx.Model(&run).Update("definition", encode(next)).Error; e != nil {
		return "", e
	}
	return id, nil
}
func (s *Server) closeProfileIssue(tx *gorm.DB, p identity.Principal, issue GovernanceQualityIssue, note string) error {
	if issue.RetestRunID == nil {
		return problem(409, "QUALITY_RETEST_REQUIRED", "必须完成真实复测后关闭")
	}
	evidence, e := decode[governanceIssueEvidence](issue.Evidence)
	if e != nil {
		return e
	}
	var run GovernanceRun
	if e = tx.Where("id=? AND department_id=?", *issue.RetestRunID, issue.DepartmentID).Take(&run).Error; e != nil {
		return e
	}
	if run.Kind != "profile" || run.State != "succeeded" || !run.CreatedAt.After(issue.CreatedAt) {
		return problem(409, "QUALITY_RETEST_REQUIRED", "较新的完整复测尚未成功")
	}
	def, e := decode[governanceProfileDefinition](run.Definition)
	if e != nil {
		return e
	}
	original := evidence.Profile
	if def.Mode != "full" || !governanceIssueInputMatches(def.Input, original.Input) || !reflect.DeepEqual(def.StandardBindings, original.StandardBindings) || def.BusinessTimeField != original.BusinessTimeField {
		return problem(409, "QUALITY_RETEST_EVIDENCE", "复测与原来源、字段、参数或固定标准不一致")
	}
	actor, e := s.etlPrincipal(tx.Statement.Context, run.ActorID)
	if e != nil {
		return e
	}
	if e = s.governanceCheckInput(governanceReadContext(tx.Statement.Context), actor, def.Input, false); e != nil {
		return e
	}
	report, e := decode[ProfileReport](run.Result)
	if e != nil {
		return e
	}
	if report.Coverage != "full" || report.Rows != report.TotalRows {
		return conflict
	}
	if issue.OriginKind == "standard" {
		if report.StandardAudit == nil || report.StandardAudit.RejectedRows != 0 {
			return problem(409, "QUALITY_RETEST_FAILED", "复测仍有标准违规")
		}
		_, _, e = resolveStandardBindings(tx, p, def.StandardBindings)
		return e
	}
	if issue.MonitorID == nil || issue.ObservationID == nil {
		return invalid
	}
	monitor, e := monitorAccess(tx, p, *issue.MonitorID, true)
	if e != nil {
		return e
	}
	if monitor.Version != evidence.PolicyVersion || fingerprint(monitor.Policy) != fingerprint(evidence.Policy) || !reflect.DeepEqual(monitor.BaselineID, evidence.BaselineID) {
		return problem(409, "MONITOR_ISSUE_CONFIGURATION_CHANGED", "监测配置或固定基线已变化")
	}
	var retest GovernanceMonitorObservation
	if e = tx.Where("monitor_id=? AND run_id=?", *issue.MonitorID, run.ID).Take(&retest).Error; e != nil {
		return e
	}
	if retest.State != "passed" || retest.PolicyVersion != evidence.PolicyVersion {
		return problem(409, "QUALITY_RETEST_FAILED", "监测尚未生成正常复测结论")
	}
	var originalObservation GovernanceMonitorObservation
	if e = tx.Where("id=?", *issue.ObservationID).Take(&originalObservation).Error; e != nil {
		return e
	}
	if originalObservation.ResolutionState == "closed" {
		return conflict
	}
	originalObservation.ResolutionState = "closed"
	originalObservation.ResolutionNote = note
	originalObservation.RetestObservationID = &retest.ID
	originalObservation.Version++
	return tx.Save(&originalObservation).Error
}
func governanceProfileIssueKind(kind string) bool {
	return slices.Contains([]string{"monitor", "standard"}, kind)
}

func governanceIssueInputMatches(actual, original governanceInput) bool {
	return actual.Resource == original.Resource && actual.ResourceFingerprint == original.ResourceFingerprint && actual.SourceVersion == original.SourceVersion && actual.SourceFingerprint == original.SourceFingerprint && actual.MasterSnapshotID == original.MasterSnapshotID && actual.WorkflowParent == original.WorkflowParent && reflect.DeepEqual(actual.Fields, original.Fields) && reflect.DeepEqual(actual.Parameters, original.Parameters)
}
