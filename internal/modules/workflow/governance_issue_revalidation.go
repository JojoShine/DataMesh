package workflow

import (
	"errors"
	"reflect"
	"time"

	"datamesh/internal/modules/identity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func governanceIssueFlowMatches(actual, original governanceFlowExecution) bool {
	if actual.Flow != original.Flow || actual.Trial != nil || original.Trial != nil || actual.WorkflowTrial != nil || original.WorkflowTrial != nil || actual.Preview != nil || original.Preview != nil || len(actual.Inputs) != len(original.Inputs) {
		return false
	}
	if fingerprint(encode(actual.Parameters)) != fingerprint(encode(original.Parameters)) || fingerprint(encode(actual.Pinned)) != fingerprint(encode(original.Pinned)) {
		return false
	}
	for id, input := range original.Inputs {
		current, ok := actual.Inputs[id]
		if !ok || !governanceIssueInputMatches(current, input) {
			return false
		}
	}
	return true
}

func (s *Server) retestFlowIssue(tx *gorm.DB, p identity.Principal, issue GovernanceQualityIssue) (string, error) {
	var original GovernanceRun
	if e := tx.Where("id=?", issue.RunID).Take(&original).Error; e != nil {
		return "", e
	}
	def, e := decode[governanceFlowExecution](original.Definition)
	if e != nil {
		return "", e
	}
	// A release trial must stay under its proposal; it must never create a published output via this endpoint.
	if def.Trial != nil || def.WorkflowTrial != nil || def.Preview != nil || def.StreamSnapshot != nil {
		return "", problem(409, "QUALITY_RETEST_TRIAL_CONTEXT", "请从原变更单或流任务重新执行验证，试跑不能转为正式复测")
	}
	bindings := map[string]string{}
	parent := ""
	for id, input := range def.Inputs {
		if input.WorkflowParent != "" {
			if parent != "" && parent != input.WorkflowParent {
				return "", invalid
			}
			parent = input.WorkflowParent
			bindings[id] = input.Resource.ID
		}
	}
	_, raw, id, e := s.enqueueGovernanceFlowBindings(tx, p, def.Flow, encode(governanceFlowRunInput{Parameters: def.Parameters}), bindings, parent)
	if e != nil {
		return "", e
	}
	run, e := decode[GovernanceRun](raw)
	if e != nil {
		return "", e
	}
	next, e := decode[governanceFlowExecution](run.Definition)
	if e != nil {
		return "", e
	}
	if !governanceIssueFlowMatches(next, def) {
		return "", problem(409, "QUALITY_RETEST_EVIDENCE", "原输入契约或固定规则已变化，请用新完整验证替代原问题")
	}
	return id, nil
}

// Supersession records a change of validation basis, never a passing result for the old evidence.
func (s *Server) supersedeQualityIssue(tx *gorm.DB, p identity.Principal, issue *GovernanceQualityIssue, replacementID, note string) error {
	if !valid(replacementID, 128) || replacementID == issue.RunID {
		return invalid
	}
	var original GovernanceRun
	if e := tx.Where("id=?", issue.RunID).Take(&original).Error; e != nil {
		return e
	}
	next, e := governanceRunRead(tx, p, replacementID, false)
	if e != nil {
		return e
	}
	if next.WorkspaceID != original.WorkspaceID || next.DepartmentID != issue.DepartmentID || next.Kind != original.Kind || next.State != "succeeded" || !next.CreatedAt.After(issue.CreatedAt) {
		return problem(409, "QUALITY_REPLACEMENT_EVIDENCE", "替代验证必须是本空间同类业务较新的完整成功运行")
	}
	failed := false
	if original.Kind == "dataflow" {
		basis, e := decode[governanceFlowExecution](original.Definition)
		if e != nil {
			return e
		}
		actual, e := decode[governanceFlowExecution](next.Definition)
		if e != nil {
			return e
		}
		if actual.Flow.ID != basis.Flow.ID || actual.Trial != nil || actual.WorkflowTrial != nil || actual.Preview != nil || actual.StreamSnapshot != nil || actual.Lightweight != nil || len(actual.Inputs) != len(basis.Inputs) || governanceIssueFlowMatches(actual, basis) {
			return problem(409, "QUALITY_REPLACEMENT_BASIS", "同一流程的新依据才能替代；原依据未变请使用复测关闭")
		}
		for id, old := range basis.Inputs {
			current, ok := actual.Inputs[id]
			if !ok || current.Resource.ID != old.Resource.ID || current.Resource.Type != old.Resource.Type {
				return problem(409, "QUALITY_REPLACEMENT_SOURCE", "替代验证不能更换业务来源")
			}
		}
		if e = s.governanceFlowAuthorize(governanceReadContext(tx.Statement.Context), p, actual); e != nil {
			return e
		}
		report, e := decode[governanceFlowReport](next.Result)
		if e != nil {
			return e
		}
		failed = report.RejectedRows > 0
	} else if original.Kind == "profile" && governanceProfileIssueKind(issue.OriginKind) {
		evidence, e := decode[governanceIssueEvidence](issue.Evidence)
		if e != nil {
			return e
		}
		def, e := decode[governanceProfileDefinition](next.Definition)
		if e != nil {
			return e
		}
		report, e := decode[ProfileReport](next.Result)
		if e != nil {
			return e
		}
		if def.Mode != "full" || report.Coverage != "full" || report.Rows != report.TotalRows || def.Input.Resource.ID != evidence.Profile.Input.Resource.ID || def.Input.Resource.Type != evidence.Profile.Input.Resource.Type {
			return problem(409, "QUALITY_REPLACEMENT_SOURCE", "替代验证必须完整覆盖同一业务来源")
		}
		if e = s.governanceCheckInput(governanceReadContext(tx.Statement.Context), p, def.Input, false); e != nil {
			return e
		}
		changed := !governanceIssueInputMatches(def.Input, evidence.Profile.Input) || !reflect.DeepEqual(def.StandardBindings, evidence.Profile.StandardBindings) || def.BusinessTimeField != evidence.Profile.BusinessTimeField
		if issue.OriginKind == "standard" {
			if len(def.StandardBindings) == 0 || report.StandardAudit == nil {
				return problem(409, "QUALITY_REPLACEMENT_STANDARD", "替代验证必须保留完整的标准核验依据")
			}
			// Removing the original checked fields is not a migration of their rules.
			for _, binding := range evidence.Profile.StandardBindings {
				found := false
				for _, current := range def.StandardBindings {
					if binding.Field == current.Field {
						found = true
					}
				}
				if !found {
					return problem(409, "QUALITY_REPLACEMENT_STANDARD", "替代核验不能删除原检查字段")
				}
			}
			if _, _, e = resolveStandardBindings(tx, p, def.StandardBindings); e != nil {
				return e
			}
			failed = report.StandardAudit.RejectedRows > 0
		} else {
			if issue.MonitorID == nil {
				return invalid
			}
			monitor, e := monitorAccess(tx, p, *issue.MonitorID, true)
			if e != nil {
				return e
			}
			var observation GovernanceMonitorObservation
			if e = tx.Where("monitor_id=? AND run_id=?", monitor.ID, next.ID).Take(&observation).Error; e != nil {
				return e
			}
			if observation.PolicyVersion != monitor.Version || fingerprint(observation.Policy) != fingerprint(monitor.Policy) || (observation.State != "passed" && observation.State != "alert") {
				return problem(409, "QUALITY_REPLACEMENT_MONITOR", "请选择当前监测配置已完成的验证")
			}
			changed = changed || monitor.Version != evidence.PolicyVersion || fingerprint(monitor.Policy) != fingerprint(evidence.Policy) || !reflect.DeepEqual(monitor.BaselineID, evidence.BaselineID)
			failed = observation.State == "alert"
		}
		if !changed {
			return problem(409, "QUALITY_REPLACEMENT_BASIS", "检查依据未变化，请使用原配置复测关闭")
		}
	} else {
		return invalid
	}
	// Lock the replacement issue to prevent replacing into a concurrently closed or superseded chain.
	if failed {
		var replacement GovernanceQualityIssue
		if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("run_id=?", next.ID).Take(&replacement).Error; e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return problem(409, "QUALITY_REPLACEMENT_ISSUE_REQUIRED", "新验证仍有异常，请先为新运行登记整改工单")
			}
			return e
		}
		if replacement.ID == issue.ID || governanceIssueTerminal(replacement.State) || replacement.OriginKind != issue.OriginKind {
			return conflict
		}
		if _, e = governanceIssueRead(tx, p, replacement.ID, false); e != nil {
			return e
		}
		issue.ReplacementIssueID = &replacement.ID
	}
	if issue.OriginKind == "monitor" && issue.ObservationID != nil {
		var observation GovernanceMonitorObservation
		if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", *issue.ObservationID).Take(&observation).Error; e != nil {
			return e
		}
		observation.ResolutionState = "closed"
		observation.ResolutionNote = "已由新依据验证替代（不代表原检查合格）：" + note
		observation.Version++
		if e = tx.Save(&observation).Error; e != nil {
			return e
		}
	}
	now := time.Now().UTC()
	issue.State = "superseded"
	issue.ClosedAt = &now
	issue.ReplacementRunID = &next.ID
	return nil
}

func governanceIssueResolutionComplete(tx *gorm.DB, p identity.Principal, issue GovernanceQualityIssue) (bool, error) {
	for depth := 0; depth < 32; depth++ {
		if issue.State == "closed" {
			return true, nil
		}
		if issue.State != "superseded" {
			return false, nil
		}
		if issue.ReplacementIssueID == nil {
			return issue.ReplacementRunID != nil, nil
		}
		next, e := governanceIssueRead(tx, p, *issue.ReplacementIssueID, false)
		if e != nil {
			return false, e
		}
		issue = next
	}
	return false, problem(409, "QUALITY_REPLACEMENT_DEPTH", "替代链过长，请核对后续整改进展")
}
